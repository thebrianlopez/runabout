package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// BT-3: InitConfig creates scaffold file at a new path
func TestInitConfig_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	err := InitConfig(path, false)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotEmpty(t, data)
	// Scaffold must be parseable as YAML (no syntax errors).
	cfg, err := LoadConfig(path)
	// The scaffold has placeholder values that won't pass validation, but the
	// file must at least parse without a parse error.
	var ce *ConfigError
	if err != nil {
		require.ErrorAs(t, err, &ce)
		assert.NotEqual(t, "config_parse_error", ce.Code, "scaffold YAML must be syntactically valid")
	} else {
		assert.NotNil(t, cfg)
	}
}

// BT-4: InitConfig returns config_file_exists when file exists and force=false
func TestInitConfig_ExistingFileNoForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("existing"), 0o600))

	err := InitConfig(path, false)
	require.Error(t, err)
	var ce *ConfigError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "config_file_exists", ce.Code)
}

// BT-5: InitConfig overwrites existing file when force=true
func TestInitConfig_ExistingFileWithForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("old content"), 0o600))

	err := InitConfig(path, true)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "bmux configuration")
	assert.NotContains(t, string(data), "old content")
}

// InitConfig creates parent directories as needed.
func TestInitConfig_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "nested", "config.yaml")

	err := InitConfig(path, false)
	require.NoError(t, err)
	_, err = os.Stat(path)
	assert.NoError(t, err)
}
