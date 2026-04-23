package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the parsed representation of config.yaml.
type Config struct {
	Hosts     []HostConfig    `yaml:"hosts"`
	Reconnect ReconnectConfig `yaml:"reconnect"`
	Log       LogConfig       `yaml:"log"`
}

// HostConfig is a single SSH host entry. Shared with the ssh package.
type HostConfig struct {
	Name         string `yaml:"name"`          // required, unique
	SSHHost      string `yaml:"ssh_host"`      // required
	SSHUser      string `yaml:"ssh_user"`      // required
	SSHPort      int    `yaml:"ssh_port"`      // optional, default 22
	IdentityFile string `yaml:"identity_file"` // optional, ~ expanded at load time
}

// ReconnectConfig controls exponential backoff on SSH disconnect.
type ReconnectConfig struct {
	InitialInterval Duration `yaml:"initial_interval"` // default: 2s
	MaxInterval     Duration `yaml:"max_interval"`     // default: 5m
	Multiplier      float64  `yaml:"multiplier"`       // default: 2.0
}

// LogConfig controls log output format and level.
type LogConfig struct {
	Format string `yaml:"format"` // "text" | "json", default: "text"
	Level  string `yaml:"level"`  // "debug"|"info"|"warn"|"error", default: "info"
}

// Duration is a yaml-deserializable time.Duration.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	v, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	d.Duration = v
	return nil
}

// LoadConfig reads and validates config from path.
// Returns a typed *ConfigError for all validation failures.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNotFound(path)
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var raw yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, errParseError(path, 0, err.Error())
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, errParseError(path, 0, err.Error())
	}

	if err := validate(&cfg, path); err != nil {
		return nil, err
	}

	// Apply defaults.
	applyDefaults(&cfg)

	// Expand ~ in identity_file fields.
	home, _ := os.UserHomeDir()
	for i := range cfg.Hosts {
		if cfg.Hosts[i].IdentityFile != "" {
			cfg.Hosts[i].IdentityFile = expandHome(cfg.Hosts[i].IdentityFile, home)
		}
		if cfg.Hosts[i].SSHPort == 0 {
			cfg.Hosts[i].SSHPort = 22
		}
	}

	return &cfg, nil
}

func validate(cfg *Config, path string) error {
	if len(cfg.Hosts) == 0 {
		return errNoHosts()
	}

	seen := make(map[string]bool)
	for i, h := range cfg.Hosts {
		if h.Name == "" {
			return errInvalid(fmt.Sprintf("hosts[%d].name", i))
		}
		if h.SSHHost == "" {
			return errInvalid(fmt.Sprintf("hosts[%d].ssh_host", i))
		}
		if h.SSHUser == "" {
			return errInvalid(fmt.Sprintf("hosts[%d].ssh_user", i))
		}
		if seen[h.Name] {
			return errDuplicateHost(h.Name)
		}
		seen[h.Name] = true
	}

	if cfg.Reconnect.Multiplier != 0 && cfg.Reconnect.Multiplier < 1.0 {
		return &ConfigError{
			Code:    "config_invalid",
			Message: "config invalid: reconnect.multiplier must be >= 1.0",
		}
	}

	if cfg.Log.Format != "" && cfg.Log.Format != "text" && cfg.Log.Format != "json" {
		return &ConfigError{
			Code:    "config_invalid",
			Message: fmt.Sprintf("config invalid: log.format must be text or json, got %q", cfg.Log.Format),
		}
	}

	return nil
}

func applyDefaults(cfg *Config) {
	if cfg.Reconnect.InitialInterval.Duration == 0 {
		cfg.Reconnect.InitialInterval = Duration{2 * time.Second}
	}
	if cfg.Reconnect.MaxInterval.Duration == 0 {
		cfg.Reconnect.MaxInterval = Duration{5 * time.Minute}
	}
	if cfg.Reconnect.Multiplier == 0 {
		cfg.Reconnect.Multiplier = 2.0
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = "text"
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
}

func expandHome(path, home string) string {
	if len(path) == 0 {
		return path
	}
	if path == "~" {
		return home
	}
	if len(path) >= 2 && path[:2] == "~/" {
		return filepath.Join(home, path[2:])
	}
	return path
}
