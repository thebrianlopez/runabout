package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/config"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/insights"
)

func TestBuildCustomTracks_NilConfig(t *testing.T) {
	orig := fileConfig
	defer func() { fileConfig = orig }()

	fileConfig = nil
	custom := buildCustomTracks()
	if custom != nil {
		t.Errorf("expected nil, got %v", custom)
	}
}

func TestBuildCustomTracks_NoCareerLens(t *testing.T) {
	orig := fileConfig
	defer func() { fileConfig = orig }()

	fileConfig = &config.FileConfig{}
	custom := buildCustomTracks()
	if custom != nil {
		t.Errorf("expected nil, got %v", custom)
	}
}

func TestBuildCustomTracks_EmptyTracks(t *testing.T) {
	orig := fileConfig
	defer func() { fileConfig = orig }()

	fileConfig = &config.FileConfig{
		CareerLens: &config.CareerLensConfig{
			Tracks: map[string]config.TrackConfig{},
		},
	}
	custom := buildCustomTracks()
	if custom != nil {
		t.Errorf("expected nil for empty tracks, got %v", custom)
	}
}

func TestBuildCustomTracks_WithTracks(t *testing.T) {
	orig := fileConfig
	defer func() { fileConfig = orig }()

	fileConfig = &config.FileConfig{
		CareerLens: &config.CareerLensConfig{
			Tracks: map[string]config.TrackConfig{
				"security": {
					Description: "Security Engineer",
					Weights: map[string]float64{
						"incident_reduction": 0.40,
						"cross_team_impact":  0.10,
						"pr_review_ratio":    0.10,
						"multi_project_span": 0.05,
						"infra_theme_ratio":  0.10,
						"change_velocity":    0.10,
						"pr_comment_ratio":   0.05,
						"collaborator_span":  0.10,
					},
				},
			},
		},
	}

	custom := buildCustomTracks()
	if len(custom) != 1 {
		t.Fatalf("custom count = %d, want 1", len(custom))
	}
	sec, ok := custom["security"]
	if !ok {
		t.Fatal("missing security track")
	}
	if sec.Description != "Security Engineer" {
		t.Errorf("description = %q, want Security Engineer", sec.Description)
	}
	if sec.Weights["incident_reduction"] != 0.40 {
		t.Errorf("incident_reduction weight = %f, want 0.40", sec.Weights["incident_reduction"])
	}
}

func TestRunCareer_ValidatesCustomTracks_BadSum(t *testing.T) {
	orig := fileConfig
	defer func() { fileConfig = orig }()

	fileConfig = &config.FileConfig{
		CareerLens: &config.CareerLensConfig{
			Tracks: map[string]config.TrackConfig{
				"bad_track": {
					Description: "Bad weights",
					Weights: map[string]float64{
						"cross_team_impact": 0.50,
						"pr_review_ratio":   0.50,
						"change_velocity":   0.50,
					},
				},
			},
		},
	}

	cmd := careerCmd()
	cmd.SetArgs([]string{"--list-tracks"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for bad weight sum")
	}
	if !strings.Contains(err.Error(), "bad_track") {
		t.Errorf("error = %q, want to contain track name", err.Error())
	}
	if !strings.Contains(err.Error(), "sum to") {
		t.Errorf("error = %q, want to contain 'sum to'", err.Error())
	}
}

func TestRunCareer_ValidatesCustomTracks_UnknownDimension(t *testing.T) {
	orig := fileConfig
	defer func() { fileConfig = orig }()

	fileConfig = &config.FileConfig{
		CareerLens: &config.CareerLensConfig{
			Tracks: map[string]config.TrackConfig{
				"weird": {
					Description: "Unknown dimension",
					Weights: map[string]float64{
						"made_up_metric": 1.0,
					},
				},
			},
		},
	}

	cmd := careerCmd()
	cmd.SetArgs([]string{"--list-tracks"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown dimension")
	}
	if !strings.Contains(err.Error(), "weird") {
		t.Errorf("error = %q, want to contain track name", err.Error())
	}
	if !strings.Contains(err.Error(), "unknown dimension") {
		t.Errorf("error = %q, want to contain 'unknown dimension'", err.Error())
	}
}

func TestRunCareer_ListTracks_WithCustom(t *testing.T) {
	orig := fileConfig
	defer func() { fileConfig = orig }()

	fileConfig = &config.FileConfig{
		CareerLens: &config.CareerLensConfig{
			Tracks: map[string]config.TrackConfig{
				"security": {
					Description: "Security Engineer — incident focus",
					Weights: map[string]float64{
						"cross_team_impact":  0.05,
						"pr_review_ratio":    0.10,
						"multi_project_span": 0.05,
						"infra_theme_ratio":  0.10,
						"change_velocity":    0.10,
						"incident_reduction": 0.40,
						"pr_comment_ratio":   0.10,
						"collaborator_span":  0.10,
					},
				},
			},
		},
	}

	cmd := careerCmd()
	cmd.SetArgs([]string{"--list-tracks"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// Capture stdout
	old := captureStdout(t)
	err := cmd.Execute()
	output := old()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should show all tracks including custom
	if !strings.Contains(output, "security") {
		t.Errorf("output missing custom track 'security'")
	}
	if !strings.Contains(output, "[custom]") {
		t.Errorf("output missing [custom] label")
	}
	if !strings.Contains(output, "staff") {
		t.Errorf("output missing builtin track 'staff'")
	}
	if !strings.Contains(output, "platform") {
		t.Errorf("output missing builtin track 'platform'")
	}
	if !strings.Contains(output, "manager") {
		t.Errorf("output missing builtin track 'manager'")
	}

	// Builtins should NOT have [custom] label
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "staff") && strings.Contains(line, "[custom]") {
			t.Error("builtin track 'staff' should not have [custom] label")
		}
	}
}

func TestRunCareer_ListTracks_NoCustom(t *testing.T) {
	orig := fileConfig
	defer func() { fileConfig = orig }()

	fileConfig = nil

	cmd := careerCmd()
	cmd.SetArgs([]string{"--list-tracks"})

	old := captureStdout(t)
	err := cmd.Execute()
	output := old()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(output, "staff") {
		t.Errorf("output missing 'staff'")
	}
	if strings.Contains(output, "[custom]") {
		t.Errorf("output should not contain [custom] when no custom tracks")
	}
}

func TestRunCareer_ValidCustomTracks_PassesValidation(t *testing.T) {
	orig := fileConfig
	defer func() { fileConfig = orig }()

	fileConfig = &config.FileConfig{
		CareerLens: &config.CareerLensConfig{
			Tracks: map[string]config.TrackConfig{
				"security": {
					Description: "Security Engineer",
					Weights: map[string]float64{
						"cross_team_impact":  0.05,
						"pr_review_ratio":    0.10,
						"multi_project_span": 0.05,
						"infra_theme_ratio":  0.10,
						"change_velocity":    0.10,
						"incident_reduction": 0.40,
						"pr_comment_ratio":   0.10,
						"collaborator_span":  0.10,
					},
				},
			},
		},
	}

	custom := buildCustomTracks()

	// Validation should pass
	for name, ct := range custom {
		if err := insights.ValidateTrackWeights(ct.Weights); err != nil {
			t.Errorf("valid track %q failed validation: %v", name, err)
		}
	}
}

// captureStdout redirects os.Stdout and returns a function that restores it
// and returns the captured output.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	return func() string {
		w.Close()
		os.Stdout = origStdout
		var buf bytes.Buffer
		buf.ReadFrom(r)
		return buf.String()
	}
}
