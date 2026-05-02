package mirror

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// BT-1: Idle mirror destroyed after idle_timeout_sec.
func TestBT1_IdleMirrorDestroyed(t *testing.T) {
	requireNode(t)

	idleTimeout := 200 * time.Millisecond
	m, err := NewHeadlessMirrorManager(Options{IdleTimeout: idleTimeout})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })

	require.NoError(t, m.Write("%0", []byte("hello")))
	assert.Contains(t, m.ActivePanes(), "%0")

	// Wait for idle timeout + a small buffer.
	time.Sleep(idleTimeout + 100*time.Millisecond)

	assert.NotContains(t, m.ActivePanes(), "%0", "idle pane should have been destroyed")
}

// BT-2: Write of large PTY burst does not drop data visible in snapshot.
func TestBT2_LargeBurstWrite(t *testing.T) {
	requireNode(t)
	m := newTestManager(t)

	// Write 100KB of repeated 'X' characters — the VT grid will wrap, but
	// the character must appear in the snapshot.
	burst := strings.Repeat("X", 100*1024)
	require.NoError(t, m.Write("%0", []byte(burst)))

	// Give the subprocess a moment to process the write before snapshotting.
	time.Sleep(50 * time.Millisecond)

	ansi, err := m.Snapshot("%0", 220, 50)
	require.NoError(t, err)
	assert.Contains(t, string(ansi), "X")
}

// BT-3: Multiple panes managed independently.
func TestBT3_MultiplePanesIndependent(t *testing.T) {
	requireNode(t)
	m := newTestManager(t)

	require.NoError(t, m.Write("%0", []byte("pane-zero-content")))
	require.NoError(t, m.Write("%1", []byte("pane-one-content")))

	snap0, err := m.Snapshot("%0", 80, 24)
	require.NoError(t, err)
	snap1, err := m.Snapshot("%1", 80, 24)
	require.NoError(t, err)

	assert.Contains(t, string(snap0), "pane-zero-content")
	assert.Contains(t, string(snap1), "pane-one-content")
	assert.NotContains(t, string(snap0), "pane-one-content")
	assert.NotContains(t, string(snap1), "pane-zero-content")
}

// RG-1: Subprocess restart does not corrupt post-restart writes.
func TestRG1_RestartNoCorruption(t *testing.T) {
	requireNode(t)
	m := newTestManager(t)
	impl := m.(*manager)

	require.NoError(t, m.Write("%0", []byte("before")))

	// Kill subprocess.
	impl.mu.Lock()
	proc := impl.cmd
	impl.mu.Unlock()
	_ = proc.Process.Kill()

	// Wait for auto-restart.
	time.Sleep(500 * time.Millisecond)

	// Post-restart write must succeed and be visible in snapshot.
	require.NoError(t, m.Write("%0", []byte("after-restart")))
	time.Sleep(50 * time.Millisecond)

	ansi, err := m.Snapshot("%0", 80, 24)
	require.NoError(t, err)
	// Post-restart content must appear; pre-crash content is gone (state reset).
	assert.Contains(t, string(ansi), "after-restart")
}

// RG-2: Snapshot does not return stale data from a previous resize dimension.
func TestRG2_StaleResizeNotVisible(t *testing.T) {
	requireNode(t)
	m := newTestManager(t)

	// Write a 30-char line.
	require.NoError(t, m.Write("%0", []byte(strings.Repeat("Z", 30))))

	// Snapshot at 20 cols — line wraps.
	snap20, err := m.Snapshot("%0", 20, 24)
	require.NoError(t, err)
	assert.NotEmpty(t, snap20)

	// Resize to 120 cols.
	require.NoError(t, m.Resize("%0", 120, 24))

	// Snapshot at 120 cols — line should not wrap.
	snap120, err := m.Snapshot("%0", 120, 24)
	require.NoError(t, err)
	assert.NotEmpty(t, snap120)
	// The 30 Z's fit on one line at 120 cols.
	assert.Contains(t, string(snap120), strings.Repeat("Z", 30))
}
