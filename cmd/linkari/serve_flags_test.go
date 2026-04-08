package main

import (
	"io"
	"strings"
	"testing"
)

// TestServeCmdMutualExclusion asserts that --tsnet and --local are mutually
// exclusive via cobra MarkFlagsMutuallyExclusive. The error fires before RunE
// so no server logic runs. Pinned per EPIC-048 M2 blockers-to-95 blocker #1.
func TestServeCmdMutualExclusion(t *testing.T) {
	cmd := serveCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--tsnet", "--local"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error from --tsnet --local, got nil")
	}
	// cobra error format: "if any flags in the group [tsnet local] are set none of the others can be"
	if !strings.Contains(err.Error(), "tsnet") || !strings.Contains(err.Error(), "local") {
		t.Errorf("unexpected error %q — want message mentioning tsnet and local", err.Error())
	}
}

// TestServeCmdTsnetDefault asserts that --tsnet defaults to true and --local
// defaults to false in the registered flag set.
func TestServeCmdTsnetDefault(t *testing.T) {
	cmd := serveCmd()
	tsnet := cmd.Flags().Lookup("tsnet")
	if tsnet == nil {
		t.Fatal("--tsnet flag not registered")
	}
	if tsnet.DefValue != "true" {
		t.Errorf("--tsnet DefValue=%q want %q", tsnet.DefValue, "true")
	}
	local := cmd.Flags().Lookup("local")
	if local == nil {
		t.Fatal("--local flag not registered")
	}
	if local.DefValue != "false" {
		t.Errorf("--local DefValue=%q want %q", local.DefValue, "false")
	}
}

// TestServeCmdTsnetHelpText pins the --tsnet and --local usage strings so
// operators can see at a glance that tsnet is on by default.
// Pinned per EPIC-048 M2 blockers-to-95 blocker #4.
func TestServeCmdTsnetHelpText(t *testing.T) {
	cmd := serveCmd()
	tsnetUsage := cmd.Flags().Lookup("tsnet").Usage
	if !strings.Contains(tsnetUsage, "default: true") {
		t.Errorf("--tsnet usage %q should mention 'default: true'", tsnetUsage)
	}
	localUsage := cmd.Flags().Lookup("local").Usage
	if !strings.Contains(localUsage, "disables tsnet") {
		t.Errorf("--local usage %q should mention 'disables tsnet'", localUsage)
	}
}
