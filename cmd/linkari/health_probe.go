package main

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// HealthProbe holds the result of a lit/tessdata readiness check.
// Populated by probeHealth and embedded in the GET /health response.
type HealthProbe struct {
	LitPresent        bool   // true when lit binary is on PATH
	TessdataPrefixSet bool   // true when tessdata_prefix is configured or env var is set
	LitVersion        string // output of `lit --version`; empty when lit absent or version unavailable
	Status            string // "ok" | "degraded"
}

// probeHealth checks whether the lit binary is available and tessdata is configured.
// tessDataPrefix is read from the config struct (primary); lookPath is injected so
// tests can exercise all branches without touching the real filesystem. EPIC-109 M2.
func probeHealth(lookPath func(string) (string, error), tessDataPrefix string) HealthProbe {
	probe := HealthProbe{
		TessdataPrefixSet: tessDataPrefix != "",
	}

	if _, err := lookPath("lit"); err == nil {
		probe.LitPresent = true
		// Capture `lit --version` with a 2s timeout; treat timeout/error as empty version.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var out bytes.Buffer
		cmd := exec.CommandContext(ctx, "lit", "--version")
		cmd.Stdout = &out
		if runErr := cmd.Run(); runErr == nil {
			probe.LitVersion = strings.TrimSpace(out.String())
		}
	}

	if !probe.LitPresent || !probe.TessdataPrefixSet {
		probe.Status = "degraded"
	} else {
		probe.Status = "ok"
	}
	return probe
}
