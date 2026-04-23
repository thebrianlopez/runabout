package config

import "fmt"

// ConfigError is a typed error from config loading or validation.
type ConfigError struct {
	Code    string
	Message string
}

func (e *ConfigError) Error() string { return e.Message }

func errNotFound(path string) *ConfigError {
	return &ConfigError{
		Code:    "config_not_found",
		Message: fmt.Sprintf("no config found at %s — run: bmux config init", path),
	}
}

func errParseError(path string, line int, detail string) *ConfigError {
	return &ConfigError{
		Code:    "config_parse_error",
		Message: fmt.Sprintf("config parse error at %s:%d: %s", path, line, detail),
	}
}

func errInvalid(field string) *ConfigError {
	return &ConfigError{
		Code:    "config_invalid",
		Message: fmt.Sprintf("config invalid: %s is required", field),
	}
}

func errNoHosts() *ConfigError {
	return &ConfigError{
		Code:    "config_no_hosts",
		Message: "config invalid: hosts list must contain at least one entry",
	}
}

func errDuplicateHost(name string) *ConfigError {
	return &ConfigError{
		Code:    "config_duplicate_host",
		Message: fmt.Sprintf("config invalid: duplicate host name %q", name),
	}
}

func errFileExists(path string) *ConfigError {
	return &ConfigError{
		Code:    "config_file_exists",
		Message: fmt.Sprintf("config already exists at %s — use --force to overwrite", path),
	}
}
