package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/blo-grindr/bmux/internal/daemon"
)

// runArgs executes the root command with the given arguments and returns
// stdout, stderr, and the error returned by Execute.
// It always uses fresh temp dirs for XDG paths so tests are isolated.
func runArgs(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))

	root := newRootCmd()
	outBuf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	root.SetOut(outBuf)
	root.SetErr(errBuf)
	root.SetArgs(args)
	root.SilenceUsage = true
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// --- Contract Tests ---

// CT-1: --version prints the version string and exits 0.
func TestCLI_Version(t *testing.T) {
	stdout, _, err := runArgs(t, "--version")
	require.NoError(t, err)
	assert.Contains(t, stdout, "bmux")
	assert.Contains(t, stdout, version)
}

// CT-2: --log-format json is accepted without error.
func TestCLI_LogFormatJSON(t *testing.T) {
	// Use socket-path as a harmless subcommand that triggers PersistentPreRunE.
	_, _, err := runArgs(t, "--log-format", "json", "socket-path")
	assert.NoError(t, err)
}

// CT-3: --log-level debug and -v shorthand are accepted without error.
func TestCLI_LogLevel(t *testing.T) {
	_, _, err := runArgs(t, "--log-level", "debug", "socket-path")
	assert.NoError(t, err)
}

func TestCLI_Verbose(t *testing.T) {
	_, _, err := runArgs(t, "-v", "socket-path")
	assert.NoError(t, err)
}

// CT-4: config init creates the config file when it does not exist.
func TestCLI_ConfigInit_CreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))

	root := newRootCmd()
	root.SetArgs([]string{"config", "init"})
	root.SilenceUsage = true
	err := root.Execute()
	require.NoError(t, err)

	configPath := filepath.Join(tmpDir, "config", "bmux", "config.yaml")
	_, statErr := os.Stat(configPath)
	assert.NoError(t, statErr, "config file should exist after init")
}

// CT-5: config init without --force exits non-zero when file already exists.
func TestCLI_ConfigInit_ExistsNoForce(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))

	// Create the file first.
	configDir := filepath.Join(tmpDir, "config", "bmux")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("existing"), 0o600))

	root := newRootCmd()
	root.SetArgs([]string{"config", "init"})
	root.SilenceUsage = true
	err := root.Execute()
	require.Error(t, err, "should error when file already exists without --force")
}

// CT-6: config init --force overwrites an existing config file.
func TestCLI_ConfigInit_Force(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))

	configDir := filepath.Join(tmpDir, "config", "bmux")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("old"), 0o600))

	root := newRootCmd()
	root.SetArgs([]string{"config", "init", "--force"})
	root.SilenceUsage = true
	err := root.Execute()
	require.NoError(t, err)

	data, readErr := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	require.NoError(t, readErr)
	assert.NotEqual(t, "old", string(data), "file should be overwritten")
}

// CT-7: doctor exits 0 when all checks pass.
func TestCLI_Doctor_AllPass(t *testing.T) {
	checks := []DoctorCheck{
		{Name: "check-a", Run: func() DoctorResult { return DoctorResult{OK: true, Message: "ok"} }},
		{Name: "check-b", Run: func() DoctorResult { return DoctorResult{OK: true, Message: "ok"} }},
	}
	outBuf := &bytes.Buffer{}
	cmd := newDoctorCmdWithChecks(checks)
	cmd.SetOut(outBuf)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	assert.NoError(t, err)
}

// CT-8: doctor exits 1 when at least one check fails.
func TestCLI_Doctor_SomeFail(t *testing.T) {
	checks := []DoctorCheck{
		{Name: "check-ok", Run: func() DoctorResult { return DoctorResult{OK: true, Message: "ok"} }},
		{Name: "check-fail", Run: func() DoctorResult { return DoctorResult{OK: false, Message: "missing"} }},
	}
	outBuf := &bytes.Buffer{}
	cmd := newDoctorCmdWithChecks(checks)
	cmd.SetOut(outBuf)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	require.Error(t, err, "doctor should return error when any check fails")
}

// CT-9: socket-path prints StateHome path followed by a newline.
func TestCLI_SocketPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))

	root := newRootCmd()
	outBuf := &bytes.Buffer{}
	root.SetOut(outBuf)
	root.SetArgs([]string{"socket-path"})
	root.SilenceUsage = true
	err := root.Execute()
	require.NoError(t, err)

	expected := filepath.Join(tmpDir, "state", "bmux")
	assert.Equal(t, expected+"\n", outBuf.String())
}

// CT-10: attach <host> execs the correct tmux command.
func TestCLI_Attach_ExecsCorrectCommand(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	stateDir := filepath.Join(tmpDir, "state", "bmux")
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))

	// Write a PID file pointing to self (so daemon appears running).
	require.NoError(t, os.MkdirAll(stateDir, 0o700))
	pidPath := filepath.Join(stateDir, "bmux.pid")
	require.NoError(t, os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600))

	// Write a status.json with the target host.
	status := &daemon.DaemonStatus{
		PID: os.Getpid(),
		Hosts: []daemon.HostStatus{{Name: "dev", Status: "connected"}},
	}
	require.NoError(t, daemon.WriteStatus(filepath.Join(stateDir, "status.json"), status))

	// Capture the exec call.
	var capturedArgv []string
	origExecFn := execFn
	t.Cleanup(func() { execFn = origExecFn })
	execFn = func(argv0 string, argv []string, envv []string) error {
		capturedArgv = argv
		return nil
	}

	root := newRootCmd()
	root.SetArgs([]string{"attach", "dev"})
	root.SilenceUsage = true
	err := root.Execute()
	require.NoError(t, err)

	require.NotNil(t, capturedArgv, "execFn should have been called")
	joined := strings.Join(capturedArgv, " ")
	assert.Contains(t, joined, "tmux")
	assert.Contains(t, joined, "attach")
	assert.Contains(t, joined, "dev")
}

// CT-11: attach <unknown-host> returns attach_unknown_host error.
func TestCLI_Attach_UnknownHost(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	stateDir := filepath.Join(tmpDir, "state", "bmux")
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))

	// Write a PID file pointing to self (daemon running) with no matching host.
	require.NoError(t, os.MkdirAll(stateDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, "bmux.pid"),
		[]byte(fmt.Sprintf("%d\n", os.Getpid())),
		0o600,
	))
	status := &daemon.DaemonStatus{
		PID:   os.Getpid(),
		Hosts: []daemon.HostStatus{{Name: "other", Status: "connected"}},
	}
	require.NoError(t, daemon.WriteStatus(filepath.Join(stateDir, "status.json"), status))

	root := newRootCmd()
	outBuf := &bytes.Buffer{}
	root.SetOut(outBuf)
	root.SetErr(outBuf)
	root.SetArgs([]string{"attach", "unknown"})
	root.SilenceUsage = true
	err := root.Execute()
	require.Error(t, err)

	var be *attachError
	require.ErrorAs(t, err, &be)
	assert.Equal(t, "attach_unknown_host", be.Code)
}

// CT-13: attach to a host that is not connected returns attach_host_not_ready.
func TestCLI_Attach_HostNotReady(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	stateDir := filepath.Join(tmpDir, "state", "bmux")
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))

	require.NoError(t, os.MkdirAll(stateDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, "bmux.pid"),
		[]byte(fmt.Sprintf("%d\n", os.Getpid())),
		0o600,
	))
	status := &daemon.DaemonStatus{
		PID:   os.Getpid(),
		Hosts: []daemon.HostStatus{{Name: "ubuntu", Status: "disconnected"}},
	}
	require.NoError(t, daemon.WriteStatus(filepath.Join(stateDir, "status.json"), status))

	root := newRootCmd()
	root.SetArgs([]string{"attach", "ubuntu"})
	root.SilenceUsage = true
	err := root.Execute()
	require.Error(t, err)

	var be *attachError
	require.ErrorAs(t, err, &be)
	assert.Equal(t, "attach_host_not_ready", be.Code)
	assert.Contains(t, be.Message, "disconnected")
}

// RG-1: attach normalizes TERM=tmux-* to xterm-256color in the exec environment.
func TestCLI_Attach_NormalizesTERM(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	stateDir := filepath.Join(tmpDir, "state", "bmux")
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))
	t.Setenv("TERM", "tmux-256color")

	require.NoError(t, os.MkdirAll(stateDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, "bmux.pid"),
		[]byte(fmt.Sprintf("%d\n", os.Getpid())),
		0o600,
	))
	status := &daemon.DaemonStatus{
		PID:   os.Getpid(),
		Hosts: []daemon.HostStatus{{Name: "dev", Status: "connected"}},
	}
	require.NoError(t, daemon.WriteStatus(filepath.Join(stateDir, "status.json"), status))

	var capturedEnv []string
	origExecFn := execFn
	t.Cleanup(func() { execFn = origExecFn })
	execFn = func(argv0 string, argv []string, envv []string) error {
		capturedEnv = envv
		return nil
	}

	root := newRootCmd()
	root.SetArgs([]string{"attach", "dev"})
	root.SilenceUsage = true
	require.NoError(t, root.Execute())

	require.NotNil(t, capturedEnv, "execFn should have been called")
	var termVal string
	for _, e := range capturedEnv {
		if strings.HasPrefix(e, "TERM=") {
			termVal = strings.TrimPrefix(e, "TERM=")
		}
	}
	assert.Equal(t, "xterm-256color", termVal, "TERM=tmux-* must be normalized to xterm-256color")
}

// CT-12: completion fish outputs a non-empty script.
func TestCLI_Completion_Fish(t *testing.T) {
	stdout, _, err := runArgs(t, "completion", "fish")
	require.NoError(t, err)
	assert.NotEmpty(t, strings.TrimSpace(stdout))
}

// --- Helper ---

// marshalStatus is a utility for tests that need to inspect JSON output.
func unmarshalStatus(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(s), &m))
	return m
}
