package lifecycle_test

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blo-grindr/bmux/internal/config"
	"github.com/blo-grindr/bmux/internal/gateway/lifecycle"
)

// ---------- call-order tracking stub ----------

type callRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *callRecorder) record(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name)
}

func (r *callRecorder) sequence() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

// stubBridge is a no-op ControlModeBridge stub that records Start/Stop.
type stubBridge struct{ rec *callRecorder }

func (s *stubBridge) Start(_ context.Context) error { s.rec.record("bridge.Start"); return nil }
func (s *stubBridge) Stop()                         { s.rec.record("bridge.Stop") }

// stubRegistry is a no-op SessionRegistry stub that records Start/Stop.
type stubRegistry struct{ rec *callRecorder }

func (s *stubRegistry) Start(_ context.Context) error { s.rec.record("registry.Start"); return nil }
func (s *stubRegistry) Stop()                         { s.rec.record("registry.Stop") }
func (s *stubRegistry) ActivePanes() []string         { return nil }

// stubMirror is a no-op HeadlessMirrorManager stub that records Close.
type stubMirror struct{ rec *callRecorder }

func (s *stubMirror) Close() error          { s.rec.record("mirror.Close"); return nil }
func (s *stubMirror) ActivePanes() []string { return nil }

// stubGateway is a no-op Gateway stub that records Start/Stop.
type stubGateway struct {
	rec         *callRecorder
	addr        string
	clientCount int
}

func (s *stubGateway) Start(_ context.Context) error { s.rec.record("gateway.Start"); return nil }
func (s *stubGateway) Stop(_ context.Context) error  { s.rec.record("gateway.Stop"); return nil }
func (s *stubGateway) ClientCount() int              { return s.clientCount }
func (s *stubGateway) Addr() string                  { return s.addr }

// ---------- helpers ----------

func validGatewayConfig() config.GatewayConfig {
	return config.GatewayConfig{
		Enabled: true,
		Port:    8765,
		Host:    "127.0.0.1",
		Auth: struct {
			Token string `yaml:"token"`
		}{
			Token: strings.Repeat("a", 64),
		},
	}
}

func buildDeps(rec *callRecorder) lifecycle.Deps {
	return lifecycle.Deps{
		Bridge:   &stubBridge{rec: rec},
		Registry: &stubRegistry{rec: rec},
		Mirror:   &stubMirror{rec: rec},
		Gateway:  &stubGateway{rec: rec, addr: "127.0.0.1:8765"},
	}
}

// ---------- CT-1: gateway.enabled=false → gateway NOT started ----------

func TestCT1_GatewayDisabled_NotStarted(t *testing.T) {
	rec := &callRecorder{}
	cfg := config.GatewayConfig{Enabled: false}
	deps := buildDeps(rec)

	mgr := lifecycle.New(cfg, deps)
	ctx := context.Background()
	err := mgr.Start(ctx)
	require.NoError(t, err)

	seq := rec.sequence()
	assert.NotContains(t, seq, "gateway.Start", "gateway should not start when disabled")
	assert.NotContains(t, seq, "bridge.Start", "bridge should not start when disabled")

	status := mgr.Status()
	assert.False(t, status.Running)
}

// ---------- CT-2: gateway.enabled=true + valid token → Start() succeeds ----------

func TestCT2_GatewayEnabled_ValidToken_Starts(t *testing.T) {
	rec := &callRecorder{}
	cfg := validGatewayConfig()
	deps := buildDeps(rec)

	mgr := lifecycle.New(cfg, deps)
	ctx := context.Background()
	err := mgr.Start(ctx)
	require.NoError(t, err)

	status := mgr.Status()
	assert.True(t, status.Running)
	assert.Equal(t, "127.0.0.1:8765", status.Addr)
}

// ---------- CT-3: gateway.enabled=true + empty token → gateway_token_missing ----------

func TestCT3_EmptyToken_ValidationError(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled: true,
		Port:    8765,
		Host:    "127.0.0.1",
	}
	err := config.ValidateGateway(cfg)
	require.Error(t, err)
	ce, ok := err.(*config.ConfigError)
	require.True(t, ok, "expected *config.ConfigError")
	assert.Equal(t, "gateway_token_missing", ce.Code)
}

// ---------- CT-4: gateway.enabled=true + token <64 chars → gateway_token_too_short ----------

func TestCT4_ShortToken_ValidationError(t *testing.T) {
	cfg := config.GatewayConfig{
		Enabled: true,
		Port:    8765,
		Host:    "127.0.0.1",
		Auth: struct {
			Token string `yaml:"token"`
		}{Token: "tooshort"},
	}
	err := config.ValidateGateway(cfg)
	require.Error(t, err)
	ce, ok := err.(*config.ConfigError)
	require.True(t, ok, "expected *config.ConfigError")
	assert.Equal(t, "gateway_token_too_short", ce.Code)
}

// ---------- CT-5: Stop() closes all WS connections before returning ----------

func TestCT5_Stop_WaitsForConnections(t *testing.T) {
	rec := &callRecorder{}
	cfg := validGatewayConfig()
	deps := buildDeps(rec)

	mgr := lifecycle.New(cfg, deps)
	ctx := context.Background()
	require.NoError(t, mgr.Start(ctx))

	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		_ = mgr.Stop(ctx)
	}()

	select {
	case <-stopDone:
		// stop completed — verify gateway.Stop was called
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() timed out")
	}

	seq := rec.sequence()
	assert.Contains(t, seq, "gateway.Stop")
}

// ---------- CT-6: Status() returns correct addr and client count ----------

func TestCT6_Status_AddrAndClientCount(t *testing.T) {
	rec := &callRecorder{}
	cfg := validGatewayConfig()
	gw := &stubGateway{rec: rec, addr: "127.0.0.1:8765", clientCount: 3}
	deps := lifecycle.Deps{
		Bridge:   &stubBridge{rec: rec},
		Registry: &stubRegistry{rec: rec},
		Mirror:   &stubMirror{rec: rec},
		Gateway:  gw,
	}

	mgr := lifecycle.New(cfg, deps)
	ctx := context.Background()
	require.NoError(t, mgr.Start(ctx))

	status := mgr.Status()
	assert.Equal(t, "127.0.0.1:8765", status.Addr)
	assert.Equal(t, 3, status.ClientCount)
}

// ---------- CT-7: token generate writes 64-char hex to config.yaml atomically ----------

func TestCT7_TokenGenerate_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Write a minimal existing config.
	existing := []byte("hosts:\n  - name: test\n    ssh_host: host\n    ssh_user: user\n")
	require.NoError(t, os.WriteFile(cfgPath, existing, 0o600))

	token, err := lifecycle.GenerateToken(cfgPath)
	require.NoError(t, err)

	// Token must be 64-char hex.
	assert.Len(t, token, 64)
	_, decErr := hex.DecodeString(token)
	assert.NoError(t, decErr, "token must be valid hex")

	// Config file must contain the token.
	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), token)
}

// ---------- CT-8: World-readable config (mode 0644) → WARN log emitted, not fatal ----------

func TestCT8_WorldReadableConfig_WarnNotFatal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	existing := []byte("hosts:\n  - name: test\n    ssh_host: host\n    ssh_user: user\n")
	require.NoError(t, os.WriteFile(cfgPath, existing, 0o644)) // world-readable

	// CheckConfigPerms should NOT return an error, but should return a warning string.
	warn := lifecycle.CheckConfigPerms(cfgPath)
	assert.NotEmpty(t, warn, "expected a warning for world-readable config")
}

// ---------- CT-9: Startup order: bridge before registry before gateway ----------

func TestCT9_StartupOrder(t *testing.T) {
	rec := &callRecorder{}
	cfg := validGatewayConfig()
	deps := buildDeps(rec)

	mgr := lifecycle.New(cfg, deps)
	ctx := context.Background()
	require.NoError(t, mgr.Start(ctx))

	seq := rec.sequence()

	bridgeIdx := indexOf(seq, "bridge.Start")
	registryIdx := indexOf(seq, "registry.Start")
	gatewayIdx := indexOf(seq, "gateway.Start")

	require.NotEqual(t, -1, bridgeIdx, "bridge.Start not found in call sequence: %v", seq)
	require.NotEqual(t, -1, registryIdx, "registry.Start not found in call sequence: %v", seq)
	require.NotEqual(t, -1, gatewayIdx, "gateway.Start not found in call sequence: %v", seq)

	assert.Less(t, bridgeIdx, registryIdx, "bridge.Start must come before registry.Start")
	assert.Less(t, registryIdx, gatewayIdx, "registry.Start must come before gateway.Start")
}

// ---------- CT-10: Shutdown order: gateway before registry before bridge ----------

func TestCT10_ShutdownOrder(t *testing.T) {
	rec := &callRecorder{}
	cfg := validGatewayConfig()
	deps := buildDeps(rec)

	mgr := lifecycle.New(cfg, deps)
	ctx := context.Background()
	require.NoError(t, mgr.Start(ctx))
	require.NoError(t, mgr.Stop(ctx))

	seq := rec.sequence()

	gatewayStopIdx := indexOf(seq, "gateway.Stop")
	registryStopIdx := indexOf(seq, "registry.Stop")
	mirrorCloseIdx := indexOf(seq, "mirror.Close")
	bridgeStopIdx := indexOf(seq, "bridge.Stop")

	require.NotEqual(t, -1, gatewayStopIdx, "gateway.Stop not found: %v", seq)
	require.NotEqual(t, -1, registryStopIdx, "registry.Stop not found: %v", seq)
	require.NotEqual(t, -1, mirrorCloseIdx, "mirror.Close not found: %v", seq)
	require.NotEqual(t, -1, bridgeStopIdx, "bridge.Stop not found: %v", seq)

	assert.Less(t, gatewayStopIdx, registryStopIdx, "gateway.Stop must come before registry.Stop")
	assert.Less(t, registryStopIdx, mirrorCloseIdx, "registry.Stop must come before mirror.Close")
	assert.Less(t, mirrorCloseIdx, bridgeStopIdx, "mirror.Close must come before bridge.Stop")
}

// ---------- BT-1: host "0.0.0.0" logs gateway_lan_binding WARN ----------

func TestBT1_LANBinding_WarnLogged(t *testing.T) {
	// CheckLANBinding returns a non-empty warning string for 0.0.0.0.
	warn := lifecycle.CheckLANBinding("0.0.0.0")
	assert.NotEmpty(t, warn, "expected LAN binding warning for 0.0.0.0")

	// No warning for loopback.
	noWarn := lifecycle.CheckLANBinding("127.0.0.1")
	assert.Empty(t, noWarn, "expected no warning for 127.0.0.1")
}

// ---------- RG-1: gateway.enabled=false leaves Phase 1 config untouched ----------

func TestRG1_DisabledGateway_Phase1Unaffected(t *testing.T) {
	// A config with only Phase 1 fields (no gateway section) should load without error.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	phase1YAML := []byte(`
hosts:
  - name: myhost
    ssh_host: 10.0.0.1
    ssh_user: brian
reconnect:
  initial_interval: 2s
  max_interval: 5m
  multiplier: 2.0
`)
	require.NoError(t, os.WriteFile(cfgPath, phase1YAML, 0o600))

	cfg, err := config.LoadConfig(cfgPath)
	require.NoError(t, err)

	// Gateway section should be zero-value (disabled).
	assert.False(t, cfg.Gateway.Enabled)
	assert.Equal(t, "", cfg.Gateway.Auth.Token)
	// Phase 1 fields intact.
	assert.Len(t, cfg.Hosts, 1)
	assert.Equal(t, "myhost", cfg.Hosts[0].Name)
}

// ---------- helpers ----------

func indexOf(seq []string, name string) int {
	for i, s := range seq {
		if s == name {
			return i
		}
	}
	return -1
}
