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
	TessdataPrefixSet bool   // true when TESSDATA_PREFIX env var is non-empty
	LitVersion        string // output of `lit --version`; empty when lit absent or version unavailable
	Status            string // "ok" | "degraded"
}

// probeHealth checks whether the lit binary is available and TESSDATA_PREFIX is set.
// lookPath and getenv are injected so tests can exercise all branches without
// touching the real filesystem or environment.
func probeHealth(lookPath func(string) (string, error), getenv func(string) string) HealthProbe {
	probe := HealthProbe{
		TessdataPrefixSet: getenv("TESSDATA_PREFIX") != "",
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
