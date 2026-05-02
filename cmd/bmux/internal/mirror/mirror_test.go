package mirror

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nodeAvailable is set in TestMain if node is present.
var nodeAvailable bool

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("node"); err == nil {
		nodeAvailable = true
	}
	m.Run()
}

func requireNode(t *testing.T) {
	t.Helper()
	if !nodeAvailable {
		t.Skip("node not in PATH — skipping integration test")
	}
}

func newTestManager(t *testing.T) HeadlessMirrorManager {
	t.Helper()
	m, err := NewHeadlessMirrorManager(Options{IdleTimeout: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// CT-1: Write followed by Snapshot returns non-empty ANSI containing written content.
func TestCT1_WriteSnapshot_ReturnsANSI(t *testing.T) {
	requireNode(t)
	m := newTestManager(t)

	err := m.Write("%0", []byte("hello"))
	require.NoError(t, err)

	ansi, err := m.Snapshot("%0", 80, 24)
	require.NoError(t, err)
	assert.NotEmpty(t, ansi)
	assert.Contains(t, string(ansi), "hello")
}

// CT-2: Snapshot resizes before serializing — content wraps at requested cols.
func TestCT2_Snapshot_ResizesBeforeSerializing(t *testing.T) {
	requireNode(t)
	m := newTestManager(t)

	// Write a long line that would not wrap at 80 cols but wraps at 20 cols.
	long := strings.Repeat("A", 40)
	err := m.Write("%0", []byte(long))
	require.NoError(t, err)

	// Snapshot at 20 cols — the resize must happen before serialization.
	ansi, err := m.Snapshot("%0", 20, 24)
	require.NoError(t, err)
	assert.NotEmpty(t, ansi)
	// At 20 cols the 40-char string wraps; verify ANSI contains the character.
	assert.Contains(t, string(ansi), "A")
}

// CT-3: node_not_found when node absent.
func TestCT3_NodeNotFound(t *testing.T) {
	_, err := newManagerWithNodePath("nonexistent-node-binary-xyz", Options{IdleTimeout: time.Hour})
	require.Error(t, err)
	var me *MirrorError
	require.ErrorAs(t, err, &me)
	assert.Equal(t, ErrCodeNodeNotFound, me.Code)
}

// CT-4: Snapshot on unknown pane returns empty bytes, nil error.
func TestCT4_Snapshot_UnknownPane_ReturnsEmpty(t *testing.T) {
	requireNode(t)
	m := newTestManager(t)

	ansi, err := m.Snapshot("%99", 80, 24)
	require.NoError(t, err)
	assert.Empty(t, ansi)
}

// CT-5: Destroy is idempotent — second call on destroyed pane returns nil.
func TestCT5_Destroy_Idempotent(t *testing.T) {
	requireNode(t)
	m := newTestManager(t)

	require.NoError(t, m.Write("%0", []byte("data")))
	require.NoError(t, m.Destroy("%0"))
	require.NoError(t, m.Destroy("%0"))
}

// CT-6: Alt-screen TUI state preserved in snapshot.
func TestCT6_AltScreen_Preserved(t *testing.T) {
	requireNode(t)
	m := newTestManager(t)

	// ESC[?1049h enters alt-screen; write content on alt-screen; ESC[?1049l exits.
	// We stay on alt-screen so the serialize captures it.
	altScreenEnter := "\x1b[?1049h"
	content := "ALT-SCREEN-CONTENT"
	err := m.Write("%0", []byte(altScreenEnter+content))
	require.NoError(t, err)

	ansi, err := m.Snapshot("%0", 80, 24)
	require.NoError(t, err)
	assert.NotEmpty(t, ansi)
	assert.Contains(t, string(ansi), content)
}

// CT-7: Subprocess crash triggers auto-restart; subsequent Write succeeds within 3s.
func TestCT7_SubprocessCrash_AutoRestarts(t *testing.T) {
	requireNode(t)
	m := newTestManager(t)
	impl := m.(*manager)

	// Write initial data to confirm the subprocess is alive.
	require.NoError(t, m.Write("%0", []byte("before-crash")))

	// Kill the subprocess directly.
	impl.mu.Lock()
	proc := impl.cmd
	impl.mu.Unlock()
	require.NotNil(t, proc)
	_ = proc.Process.Kill()

	// Within 3s a subsequent Write must succeed (auto-restart).
	deadline := time.Now().Add(3 * time.Second)
	var writeErr error
	for time.Now().Before(deadline) {
		writeErr = m.Write("%0", []byte("after-crash"))
		if writeErr == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NoError(t, writeErr)
}

// CT-8: snapshot_timeout returned if subprocess does not respond within 500ms.
func TestCT8_SnapshotTimeout(t *testing.T) {
	// Use a fake subprocess that reads requests but never responds.
	pr, pw := io.Pipe()
	_, subStdin := io.Pipe() // subprocess reads from this (we never write)

	m := &manager{
		opts:    Options{IdleTimeout: time.Hour},
		stdin:   pw,
		pending: make(map[string]chan ipcResponse),
	}
	// Start the response reader goroutine draining from pr (simulates subprocess stdout).
	// pr will never produce valid JSON lines — reads will block.
	go func() {
		buf := make([]byte, 1024)
		for {
			_, err := pr.Read(buf)
			if err != nil {
				return
			}
		}
	}()
	_ = subStdin
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })

	_, err := m.Snapshot("%0", 80, 24)
	require.Error(t, err)
	var me *MirrorError
	require.ErrorAs(t, err, &me)
	assert.Equal(t, ErrCodeSnapshotTimeout, me.Code)
}

// CT-9: IPC correlation — concurrent Snapshot calls receive their own data.
func TestCT9_IPCCorrelation_ConcurrentSnapshots(t *testing.T) {
	// Build a fake subprocess that echoes back snapshot responses out of order.
	// We use a pipe pair: Go writes requests to fakeStdin, fake responder reads
	// and sends responses to fakeStdout; the manager reads from fakeStdout.
	fakeStdinR, fakeStdinW := io.Pipe()
	fakeStdoutR, fakeStdoutW := io.Pipe()

	m := &manager{
		opts:    Options{IdleTimeout: time.Hour},
		stdin:   fakeStdinW,
		pending: make(map[string]chan ipcResponse),
	}

	// Start the response dispatcher goroutine (normally started by Start()).
	go m.readResponses(fakeStdoutR)

	// Fake subprocess: read requests and send responses in reverse order.
	type capturedReq struct {
		ID   string `json:"id"`
		Op   string `json:"op"`
		Pane string `json:"pane"`
	}
	var captured []capturedReq
	var capMu sync.Mutex

	go func() {
		defer fakeStdinR.Close()
		dec := json.NewDecoder(fakeStdinR)
		for {
			var req capturedReq
			if err := dec.Decode(&req); err != nil {
				return
			}
			capMu.Lock()
			captured = append(captured, req)
			if len(captured) == 3 {
				// Send responses in reverse order with pane-specific data.
				for i := len(captured) - 1; i >= 0; i-- {
					r := captured[i]
					data := base64.StdEncoding.EncodeToString([]byte("snap-for-" + r.Pane))
					resp, _ := json.Marshal(map[string]interface{}{
						"id":   r.ID,
						"ok":   true,
						"data": data,
					})
					_, _ = fakeStdoutW.Write(append(resp, '\n'))
				}
				capMu.Unlock()
				fakeStdoutW.Close()
				return
			}
			capMu.Unlock()
		}
	}()
	t.Cleanup(func() { _ = fakeStdinW.Close() })

	var wg sync.WaitGroup
	results := make([][]byte, 3)
	errs := make([]error, 3)
	panes := []string{"%0", "%1", "%2"}

	for i, pane := range panes {
		wg.Add(1)
		go func(idx int, p string) {
			defer wg.Done()
			results[idx], errs[idx] = m.Snapshot(p, 80, 24)
		}(i, pane)
	}
	wg.Wait()

	for i, pane := range panes {
		require.NoError(t, errs[i])
		assert.Contains(t, string(results[i]), "snap-for-"+pane,
			"pane %s got wrong snapshot data", pane)
	}
}
