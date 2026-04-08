// EPIC-048 M1: plain-value resolver helpers for non-secret server config fields.
//
// These are secret-free siblings of resolveServerField (server_resolve.go).
// They must NOT receive a ctx or *secrets.Resolver — doing so would lie about
// the contract: bool/int/string plain fields never produce secretsmanager://
// or file:// URIs.
//
// Resolution order for all three helpers:
//
//	CLI flag (if explicitly set) > env var (if non-empty) > yaml (if non-nil/non-empty) > built-in default
//
// Tier and src return values are identical strings ("flag", "env", "yaml", "default")
// since there is no secret URI to report for plain-value fields.
package main

import "fmt"

// resolveBoolField resolves a bool field through the four-tier pipeline.
//
//   - flag is the cobra-bound variable value.
//   - flagSet indicates the flag was explicitly passed by the operator
//     (use cmd.Flags().Changed("flagname")).
//   - env is os.Getenv("LINKARI_…"); empty string means unset.
//   - yaml is the parsed *bool from ServerConfig; nil means the key was absent.
//   - def is the built-in default.
//
// env is parsed as true for exactly "1" or "true" (case-insensitive).
// Any other non-empty env value (e.g. "0", "false") is treated as false.
func resolveBoolField(flag, flagSet bool, env string, yaml *bool, def bool) (value bool, tier string, src string) {
	if flagSet {
		return flag, "flag", "flag"
	}
	if env != "" {
		v := env == "1" || env == "true" || env == "True" || env == "TRUE"
		return v, "env", "env"
	}
	if yaml != nil {
		return *yaml, "yaml", "yaml"
	}
	return def, "default", "default"
}

// resolveIntField resolves an int field through the four-tier pipeline.
//
//   - flag is the cobra-bound variable value.
//   - flagSet indicates the flag was explicitly passed by the operator.
//   - env is os.Getenv("LINKARI_…"); empty string means unset.
//   - yaml is a *int pointing at the parsed yaml field; nil means absent or zero-value-as-unset.
//   - def is the built-in default.
//
// If env is non-empty but cannot be parsed as an integer, the env tier is
// skipped and resolution falls through to yaml/default.
func resolveIntField(flag int, flagSet bool, env string, yaml *int, def int) (value int, tier string, src string) {
	if flagSet {
		return flag, "flag", "flag"
	}
	if env != "" {
		var v int
		if _, err := fmt.Sscanf(env, "%d", &v); err == nil {
			return v, "env", "env"
		}
		// Unparseable env value — fall through to yaml/default.
	}
	if yaml != nil {
		return *yaml, "yaml", "yaml"
	}
	return def, "default", "default"
}

// resolveStringField resolves a string field through the four-tier pipeline.
//
// Unlike resolveBoolField/resolveIntField, there is no flagSet parameter —
// an empty flag string is treated as "not set". Callers needing explicit
// empty-string overrides must use a flag+flagSet pattern instead.
//
//   - flag is the cobra-bound variable (empty → not set).
//   - env is os.Getenv("LINKARI_…"); empty string means unset.
//   - yaml is the parsed string field from ServerConfig; empty means absent.
//   - def is the built-in default.
func resolveStringField(flag, env, yaml, def string) (value string, tier string, src string) {
	if flag != "" {
		return flag, "flag", "flag"
	}
	if env != "" {
		return env, "env", "env"
	}
	if yaml != "" {
		return yaml, "yaml", "yaml"
	}
	return def, "default", "default"
}
