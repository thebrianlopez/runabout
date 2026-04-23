package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

const validYAML = `
hosts:
  - name: prod
    ssh_host: example.com
    ssh_user: ubuntu
    ssh_port: 2222
    identity_file: /home/user/.ssh/id_ed25519
`

// CT-7: LoadConfig returns parsed Config on valid YAML
func TestLoadConfig_ValidYAML(t *testing.T) {
	path := writeTemp(t, validYAML)
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.Hosts, 1)
	h := cfg.Hosts[0]
	assert.Equal(t, "prod", h.Name)
	assert.Equal(t, "example.com", h.SSHHost)
	assert.Equal(t, "ubuntu", h.SSHUser)
	assert.Equal(t, 2222, h.SSHPort)
	assert.Equal(t, "/home/user/.ssh/id_ed25519", h.IdentityFile)
}

// CT-8: LoadConfig returns config_invalid with field name when required field missing
func TestLoadConfig_MissingSSHHost(t *testing.T) {
	yaml := `
hosts:
  - name: prod
    ssh_user: ubuntu
`
	path := writeTemp(t, yaml)
	_, err := LoadConfig(path)
	require.Error(t, err)
	var ce *ConfigError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "config_invalid", ce.Code)
	assert.Contains(t, ce.Message, "ssh_host")
}

// CT-9: LoadConfig returns config_no_hosts on empty hosts list
func TestLoadConfig_EmptyHosts(t *testing.T) {
	yaml := "hosts: []\n"
	path := writeTemp(t, yaml)
	_, err := LoadConfig(path)
	require.Error(t, err)
	var ce *ConfigError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "config_no_hosts", ce.Code)
}

// CT-10: identity_file with ~ expands to absolute path
func TestLoadConfig_IdentityFileExpanded(t *testing.T) {
	yaml := `
hosts:
  - name: prod
    ssh_host: example.com
    ssh_user: ubuntu
    identity_file: ~/.ssh/id_ed25519
`
	path := writeTemp(t, yaml)
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	expected := filepath.Join(home, ".ssh", "id_ed25519")
	assert.Equal(t, expected, cfg.Hosts[0].IdentityFile)
	assert.NotContains(t, cfg.Hosts[0].IdentityFile, "~")
}

// CT-11: LoadConfig returns config_not_found on missing file
func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path/config.yaml")
	require.Error(t, err)
	var ce *ConfigError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "config_not_found", ce.Code)
}

// CT-12: LoadConfig returns config_duplicate_host on duplicate host names
func TestLoadConfig_DuplicateHostName(t *testing.T) {
	yaml := `
hosts:
  - name: prod
    ssh_host: a.example.com
    ssh_user: ubuntu
  - name: prod
    ssh_host: b.example.com
    ssh_user: ubuntu
`
	path := writeTemp(t, yaml)
	_, err := LoadConfig(path)
	require.Error(t, err)
	var ce *ConfigError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "config_duplicate_host", ce.Code)
	assert.Contains(t, ce.Message, "prod")
}

// BT-1: Reconnect defaults applied when block absent
func TestLoadConfig_ReconnectDefaults(t *testing.T) {
	yaml := `
hosts:
  - name: prod
    ssh_host: example.com
    ssh_user: ubuntu
`
	path := writeTemp(t, yaml)
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, cfg.Reconnect.InitialInterval.Duration)
	assert.Equal(t, 5*time.Minute, cfg.Reconnect.MaxInterval.Duration)
	assert.Equal(t, 2.0, cfg.Reconnect.Multiplier)
}

// BT-2: Log defaults applied when block absent
func TestLoadConfig_LogDefaults(t *testing.T) {
	yaml := `
hosts:
  - name: prod
    ssh_host: example.com
    ssh_user: ubuntu
`
	path := writeTemp(t, yaml)
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "text", cfg.Log.Format)
	assert.Equal(t, "info", cfg.Log.Level)
}

// BT-6: ssh_port defaults to 22 when absent
func TestLoadConfig_SSHPortDefault(t *testing.T) {
	yaml := `
hosts:
  - name: prod
    ssh_host: example.com
    ssh_user: ubuntu
`
	path := writeTemp(t, yaml)
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 22, cfg.Hosts[0].SSHPort)
}
