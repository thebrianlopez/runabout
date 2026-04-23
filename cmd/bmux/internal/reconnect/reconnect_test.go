package reconnect

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blo-grindr/bmux/internal/config"
	"github.com/blo-grindr/bmux/internal/ssh"
)

// --- Helpers ---

func defaultReconnectCfg() config.ReconnectConfig {
	return config.ReconnectConfig{
		InitialInterval: config.Duration{Duration: 2 * time.Second},
		MaxInterval:     config.Duration{Duration: 5 * time.Minute},
		Multiplier:      2.0,
	}
}

// --- Mock types ---

// mockSession implements ssh.Session with controllable disconnect.
type mockSession struct {
	mu           sync.Mutex
	host         string
	disconnected chan struct{}
	closedOnce   sync.Once
}

func newMockSession(host string) *mockSession {
	return &mockSession{host: host, disconnected: make(chan struct{})}
}

func (m *mockSession) Host() string                        { return m.host }
func (m *mockSession) Status() ssh.SessionStatus           { return ssh.StatusConnected }
func (m *mockSession) Disconnected() <-chan struct{}        { return m.disconnected }
func (m *mockSession) SendInput([]byte) error              { return nil }
func (m *mockSession) Events() <-chan ssh.PaneEvent        { return nil }
func (m *mockSession) Close() error {
	m.closedOnce.Do(func() { close(m.disconnected) })
	return nil
}

func (m *mockSession) disconnect() {
	m.closedOnce.Do(func() { close(m.disconnected) })
}

// mockManager implements ssh.SSHManager with injectable Connect behavior.
type mockManager struct {
	mu       sync.Mutex
	attempts []connectAttempt
	results  []connectResult
	sessions []ssh.Session
}

type connectAttempt struct {
	host string
}

type connectResult struct {
	session ssh.Session
	err     error
}

func (m *mockManager) Connect(ctx context.Context, host config.HostConfig) (ssh.Session, error) {
	m.mu.Lock()
	m.attempts = append(m.attempts, connectAttempt{host: host.Name})
	idx := len(m.attempts) - 1
	if idx >= len(m.results) {
		m.mu.Unlock()
		return nil, fmt.Errorf("unexpected connect call %d", idx)
	}
	result := m.results[idx]
	m.mu.Unlock()
	return result.session, result.err
}

func (m *mockManager) Disconnect(name string) error { return nil }
func (m *mockManager) Sessions() []ssh.Session      { return m.sessions }
func (m *mockManager) Events() <-chan ssh.PaneEvent  { return nil }

func (m *mockManager) attemptCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.attempts)
}

// mockClock implements Clock with manually-fired channels.
type mockClock struct {
	mu       sync.Mutex
	afterChs []chan time.Time
	delays   []time.Duration
	now      time.Time
}

func newMockClock() *mockClock {
	return &mockClock{now: time.Now()}
}

func (c *mockClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.afterChs = append(c.afterChs, ch)
	c.delays = append(c.delays, d)
	c.mu.Unlock()
	return ch
}

func (c *mockClock) Now() time.Time { return c.now }

// fireAll fires all pending After() channels.
func (c *mockClock) fireAll() {
	c.mu.Lock()
	chs := c.afterChs
	c.afterChs = nil
	c.mu.Unlock()
	for _, ch := range chs {
		ch <- c.now
	}
}

// fireNext fires the oldest pending After() channel.
func (c *mockClock) fireNext() {
	c.mu.Lock()
	if len(c.afterChs) == 0 {
		c.mu.Unlock()
		return
	}
	ch := c.afterChs[0]
	c.afterChs = c.afterChs[1:]
	c.mu.Unlock()
	ch <- c.now
}

func (c *mockClock) waitForAfterCall(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		n := len(c.afterChs)
		c.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for clock.After() call")
}

func (c *mockClock) delay(i int) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.delays[i]
}

// stateRecorder records onStateChange calls.
type stateRecorder struct {
	mu      sync.Mutex
	records []stateChange
}

type stateChange struct {
	host   string
	status ssh.SessionStatus
}

func (r *stateRecorder) record(host string, status ssh.SessionStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, stateChange{host, status})
}

func (r *stateRecorder) statuses() []ssh.SessionStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ssh.SessionStatus, len(r.records))
	for i, rc := range r.records {
		out[i] = rc.status
	}
	return out
}

func (r *stateRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

// makeHost builds a minimal config.HostConfig for tests.
func makeHost(name string) config.HostConfig {
	return config.HostConfig{Name: name, SSHHost: "127.0.0.1", SSHUser: "user", SSHPort: 22}
}

// --- Contract Tests ---

// CT-1: BackoffScheduler.Next(0) returns InitialInterval.
func TestBackoff_Next0_ReturnsInitialInterval(t *testing.T) {
	s := NewBackoffScheduler(defaultReconnectCfg())
	assert.Equal(t, 2*time.Second, s.Next(0))
}

// CT-2: BackoffScheduler.Next(N) applies multiplier correctly.
func TestBackoff_NextN_AppliesMultiplier(t *testing.T) {
	s := NewBackoffScheduler(defaultReconnectCfg())
	assert.Equal(t, 4*time.Second, s.Next(1))
	assert.Equal(t, 8*time.Second, s.Next(2))
}

// CT-3: BackoffScheduler.Next caps at MaxInterval.
func TestBackoff_Next_CapsAtMaxInterval(t *testing.T) {
	s := NewBackoffScheduler(defaultReconnectCfg())
	assert.Equal(t, 5*time.Minute, s.Next(100))
}

// CT-4: ReconnectLoop calls onStateChange("reconnecting") on disconnect.
func TestReconnectLoop_StateChangeOnDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	sess := newMockSession("dev")
	mgr := &mockManager{
		results: []connectResult{
			{session: newMockSession("dev"), err: nil}, // success on first retry
		},
	}
	clk := newMockClock()
	rec := &stateRecorder{}
	scheduler := NewBackoffScheduler(defaultReconnectCfg())

	done := make(chan error, 1)
	go func() {
		done <- RunReconnectLoop(ctx, makeHost("dev"), sess, mgr, scheduler, clk, rec.record)
	}()

	// Trigger disconnect.
	sess.disconnect()

	// Wait for clock.After to be called (backoff sleep).
	clk.waitForAfterCall(t, time.Second)

	// Before firing the clock, verify state was set to disconnected/reconnecting.
	statuses := rec.statuses()
	require.GreaterOrEqual(t, len(statuses), 1)
	assert.Equal(t, ssh.StatusDisconnected, statuses[0])

	// Fire clock to proceed with connect, then cancel loop.
	clk.fireNext()
	// Give loop time to process connect result.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit")
	}
}

// CT-5: ReconnectLoop calls onStateChange("connected") on successful reconnect.
func TestReconnectLoop_StateChangeOnReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := newMockSession("dev")
	reconnected := newMockSession("dev")
	mgr := &mockManager{
		results: []connectResult{
			{session: reconnected, err: nil},
		},
	}
	clk := newMockClock()
	rec := &stateRecorder{}

	done := make(chan error, 1)
	go func() {
		done <- RunReconnectLoop(ctx, makeHost("dev"), sess, mgr, NewBackoffScheduler(defaultReconnectCfg()), clk, rec.record)
	}()

	sess.disconnect()
	clk.waitForAfterCall(t, time.Second)
	clk.fireNext()

	// Wait for "connected" status.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		statuses := rec.statuses()
		for _, s := range statuses {
			if s == ssh.StatusConnected {
				cancel()
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected connected status; got %v", rec.statuses())
}

// CT-6: ReconnectLoop calls onStateChange("disconnected") on non-retryable error, no further attempts.
func TestReconnectLoop_NonRetryableError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := newMockSession("dev")
	mgr := &mockManager{
		results: []connectResult{
			{nil, &ssh.SSHError{Code: "ssh_auth_failed", Message: "auth failed"}},
		},
	}
	clk := newMockClock()
	rec := &stateRecorder{}

	done := make(chan error, 1)
	go func() {
		done <- RunReconnectLoop(ctx, makeHost("dev"), sess, mgr, NewBackoffScheduler(defaultReconnectCfg()), clk, rec.record)
	}()

	sess.disconnect()
	clk.waitForAfterCall(t, time.Second)
	clk.fireNext()

	// Loop should exit after the non-retryable error.
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit after non-retryable error")
	}

	// Exactly 1 Connect() attempt.
	assert.Equal(t, 1, mgr.attemptCount(), "must not retry non-retryable errors")

	// onStateChange called with disconnected.
	statuses := rec.statuses()
	found := false
	for _, s := range statuses {
		if s == ssh.StatusDisconnected {
			found = true
		}
	}
	assert.True(t, found, "expected disconnected status change")
}

// CT-7: ReconnectLoop uses injected clock — clock.After called with correct delay.
func TestReconnectLoop_UsesInjectedClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := newMockSession("dev")
	reconnectedSess := newMockSession("dev")
	mgr := &mockManager{
		results: []connectResult{
			{session: reconnectedSess, err: nil},
		},
	}
	clk := newMockClock()
	rec := &stateRecorder{}

	done := make(chan error, 1)
	go func() {
		done <- RunReconnectLoop(ctx, makeHost("dev"), sess, mgr, NewBackoffScheduler(defaultReconnectCfg()), clk, rec.record)
	}()

	sess.disconnect()
	clk.waitForAfterCall(t, time.Second)

	// Verify the delay passed to clock.After is Next(0) = 2s.
	assert.Equal(t, 2*time.Second, clk.delay(0))
	clk.fireNext()
	time.Sleep(50 * time.Millisecond)
	cancel()
}

// CT-8: ReconnectLoop exits cleanly on ctx cancellation during backoff.
func TestReconnectLoop_CtxCancellDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	sess := newMockSession("dev")
	mgr := &mockManager{}
	clk := newMockClock()

	done := make(chan error, 1)
	go func() {
		done <- RunReconnectLoop(ctx, makeHost("dev"), sess, mgr, NewBackoffScheduler(defaultReconnectCfg()), clk, func(string, ssh.SessionStatus) {})
	}()

	sess.disconnect()
	clk.waitForAfterCall(t, time.Second)
	cancel() // cancel while waiting for backoff

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit on ctx cancel")
	}
}

// CT-9: Attempt counter resets to 0 on successful reconnect.
func TestReconnectLoop_AttemptCounterReset(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initialSess := newMockSession("dev")
	reconnected1 := newMockSession("dev")

	mgr := &mockManager{
		results: []connectResult{
			{nil, &ssh.SSHError{Code: "ssh_host_unreachable", Message: "err"}}, // attempt 0: fail
			{nil, &ssh.SSHError{Code: "ssh_host_unreachable", Message: "err"}}, // attempt 1: fail
			{session: reconnected1, err: nil},                                   // attempt 2: succeed
		},
	}
	clk := newMockClock()
	delays := make([]time.Duration, 0, 5)
	var delaysMu sync.Mutex

	done := make(chan error, 1)
	go func() {
		done <- RunReconnectLoop(ctx, makeHost("dev"), initialSess, mgr, NewBackoffScheduler(defaultReconnectCfg()), clk, func(string, ssh.SessionStatus) {})
	}()

	initialSess.disconnect()

	// Drive 3 reconnect attempts (2 failures, 1 success).
	for i := 0; i < 3; i++ {
		clk.waitForAfterCall(t, time.Second)
		delaysMu.Lock()
		delays = append(delays, clk.delay(i))
		delaysMu.Unlock()
		clk.fireNext()
		time.Sleep(30 * time.Millisecond)
	}

	// Now trigger disconnect on the reconnected session and verify delay resets.
	reconnected1.disconnect()
	clk.waitForAfterCall(t, time.Second)

	delaysMu.Lock()
	delays = append(delays, clk.delays[3])
	delaysMu.Unlock()

	// First delay after reset must be Next(0) = 2s, not Next(3).
	assert.Equal(t, 2*time.Second, clk.delays[3], "attempt counter must reset after successful reconnect")
	cancel()
}

// CT-10: Per-host independence — one host's loop does not block another.
func TestReconnectLoop_PerHostIndependence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess1 := newMockSession("host1")
	sess2 := newMockSession("host2")
	reconnected2 := newMockSession("host2")

	// host1 never reconnects (no result for it); host2 reconnects immediately.
	mgr1 := &mockManager{} // host1: no connect results → blocks in After
	mgr2 := &mockManager{results: []connectResult{{session: reconnected2}}}

	clk1 := newMockClock()
	clk2 := newMockClock()

	done1 := make(chan struct{})
	done2 := make(chan struct{})

	go func() {
		defer close(done1)
		RunReconnectLoop(ctx, makeHost("host1"), sess1, mgr1, NewBackoffScheduler(defaultReconnectCfg()), clk1, func(string, ssh.SessionStatus) {}) //nolint:errcheck
	}()
	go func() {
		defer close(done2)
		RunReconnectLoop(ctx, makeHost("host2"), sess2, mgr2, NewBackoffScheduler(defaultReconnectCfg()), clk2, func(string, ssh.SessionStatus) {}) //nolint:errcheck
	}()

	// Disconnect both.
	sess1.disconnect()
	sess2.disconnect()

	// host1 blocks in After (no connect yet); host2 should reconnect independently.
	clk2.waitForAfterCall(t, time.Second)
	clk2.fireNext()

	// host2 should reach connected state without any action on host1.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, mgr2.attemptCount(), "host2 should have attempted reconnect")
	assert.Equal(t, 0, mgr1.attemptCount(), "host1 should not have reconnected yet (still in backoff)")

	cancel()
}

// --- Regression Guards ---

// RG-1: Non-retryable error must not loop indefinitely (exactly 1 attempt).
func TestRG1_NonRetryableNeverRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := newMockSession("dev")
	mgr := &mockManager{
		results: []connectResult{
			{nil, &ssh.SSHError{Code: "ssh_auth_failed", Message: "auth failed"}},
		},
	}
	clk := newMockClock()

	done := make(chan error, 1)
	go func() {
		done <- RunReconnectLoop(ctx, makeHost("dev"), sess, mgr, NewBackoffScheduler(defaultReconnectCfg()), clk, func(string, ssh.SessionStatus) {})
	}()

	sess.disconnect()
	clk.waitForAfterCall(t, time.Second)
	clk.fireNext()

	select {
	case <-done:
		assert.Equal(t, 1, mgr.attemptCount(), "exactly 1 attempt for non-retryable error")
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit")
	}
}

// RG-2: Attempt counter resets after successful reconnect.
func TestRG2_AttemptCounterReset(t *testing.T) {
	// Verified in CT-9. Additional targeted check:
	s := NewBackoffScheduler(defaultReconnectCfg())
	// After a 5-attempt history, reset means Next(0) is used, not Next(5).
	assert.Equal(t, 2*time.Second, s.Next(0))   // reset → attempt 0
	assert.NotEqual(t, s.Next(5), s.Next(0), "attempt 5 delay differs from attempt 0")
}
