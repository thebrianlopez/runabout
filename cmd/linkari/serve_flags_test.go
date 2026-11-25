package main

import (
	"strings"
	"testing"
)

// TestServeCmdLocalNoOpFlag pins the EPIC-272 M3 flag surface: --local stays
// registered as an accepted no-op (the linkari-k8s deployment manifest passes
// it), and the four tsnet flags plus their mutual-exclusion marker are gone.
func TestServeCmdLocalNoOpFlag(t *testing.T) {
	cmd := serveCmd()

	local := cmd.Flags().Lookup("local")
	if local == nil {
		t.Fatal("--local flag not registered")
	}
	if local.DefValue != "false" {
		t.Errorf("--local DefValue=%q want %q", local.DefValue, "false")
	}
	if !strings.Contains(local.Usage, "deprecated no-op") {
		t.Errorf("--local usage %q should say it is a deprecated no-op", local.Usage)
	}

	for _, gone := range []string{"tsnet", "tsnet-hostname", "tsnet-state-dir", "tsnet-authkey"} {
		if cmd.Flags().Lookup(gone) != nil {
			t.Errorf("--%s flag should be removed (EPIC-272 deleted the tsnet layer)", gone)
		}
	}
}
