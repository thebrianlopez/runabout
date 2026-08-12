package main

import (
	"os"
	"testing"
)

// testmain_test.go: package-level test setup.
// Pre-populates the archiveThresholdCfg cache with builtinConfig() before any test runs.
// This prevents the first call to loadArchiveThresholdConfig() from making slow AWS IMDS
// network calls (via expandConfigRefs/secrets.DefaultAWSFactory), which would hold
// archiveThresholdMu write lock for several seconds and cause scoreAsync goroutines
// in concurrent tests to time out.
// Pattern established in affb91c; extended here for global fix.
func init() {
	archiveThresholdMu.Lock()
	archiveThresholdCfg = builtinConfig()
	archiveThresholdMu.Unlock()
}

// xdgEnvVars are the ambient roots that github.com/adrg/xdg consults. They take
// precedence over $HOME, which makes them a test-isolation hazard.
var xdgEnvVars = []string{
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_CACHE_HOME",
	"XDG_STATE_HOME",
	"XDG_RUNTIME_DIR",
	"XDG_CONFIG_DIRS",
	"XDG_DATA_DIRS",
}

// TestMain clears the ambient XDG_* environment before any test runs.
//
// EPIC-259 introduced a path resolver over github.com/adrg/xdg, which honours
// XDG_CONFIG_HOME *over* $HOME. Developer environments commonly export an
// absolute XDG_CONFIG_HOME (Ghostty and fish both do), so xdg.Reload() inside
// PathResolver.Roots() ignored t.Setenv("HOME", tmpDir) and nativeConfigPath()
// resolved to the operator's real ~/.config/linkari/config.toml.
//
// Two consequences, the second worse than the first:
//
//  1. Tests that write a fixture config under a temp HOME silently loaded the
//     real one instead, failing on macOS while passing on CI.
//  2. Any test calling LoadConfig("") loaded live configuration, including the
//     tokens and secret references it carries. This was invisible on CI, where
//     no such file exists, which is why it shipped green.
//
// Clearing here rather than per-test means no future test can reintroduce the
// leak by forgetting a helper. Tests that deliberately exercise XDG behaviour
// (path_resolver_test.go CT-2) set the variables themselves with t.Setenv,
// which overrides this and is restored automatically.
func TestMain(m *testing.M) {
	for _, k := range xdgEnvVars {
		_ = os.Unsetenv(k)
	}
	os.Exit(runTests(m))
}
