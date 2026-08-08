package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type fixedResolver struct{ roots PathRoots }

func (r fixedResolver) Roots() (PathRoots, error) { return r.roots, nil }

func (r fixedResolver) Resolve(server ServerConfig) (EffectivePaths, error) {
	paths := EffectivePaths{
		ConfigDir: r.roots.Config,
		DataDir:   r.roots.Data,
		CacheDir:  r.roots.Cache,
		StateDir:  r.roots.State,
	}
	if server.DataDir != "" {
		paths.DataDir = expandTilde(server.DataDir)
	}
	if server.QueueDB != "" {
		paths.QueueDB = expandTilde(server.QueueDB)
	} else {
		paths.QueueDB = filepath.Join(paths.DataDir, "queue.db")
	}
	if server.SnapshotPath != "" {
		paths.SnapshotPath = expandTilde(server.SnapshotPath)
	} else {
		paths.SnapshotPath = filepath.Join(paths.DataDir, "backups", "latest.db")
	}
	if server.TranscriptsDir != "" {
		paths.TranscriptsDir = expandTilde(server.TranscriptsDir)
	} else {
		paths.TranscriptsDir = filepath.Join(paths.DataDir, "transcripts")
	}
	return paths, nil
}

func withPathResolver(t *testing.T, r PathResolver) {
	t.Helper()
	prev := activePathResolver
	activePathResolver = r
	t.Cleanup(func() { activePathResolver = prev })
}

func TestEffectivePathsResolve_Fixtures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		roots PathRoots
	}{
		{
			name: "macos",
			roots: PathRoots{
				Config: "/Users/alice/Library/Application Support/linkari",
				Data:   "/Users/alice/Library/Application Support/linkari-data",
				Cache:  "/Users/alice/Library/Caches/linkari",
				State:  "/Users/alice/Library/Application Support/linkari-state",
			},
		},
		{
			name: "linux",
			roots: PathRoots{
				Config: "/home/alice/.config/linkari",
				Data:   "/home/alice/.local/share/linkari",
				Cache:  "/home/alice/.cache/linkari",
				State:  "/home/alice/.local/state/linkari",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			t.Setenv("HOME", home)
			withPathResolver(t, fixedResolver{roots: tc.roots})
			paths, err := resolveEffectivePaths(&ServerConfig{
				DataDir:        "~/srv/linkari",
				QueueDB:        "",
				SnapshotPath:   "",
				TranscriptsDir: "",
			})
			if err != nil {
				t.Fatalf("resolveEffectivePaths: %v", err)
			}
			if paths.ConfigDir != tc.roots.Config || paths.CacheDir != tc.roots.Cache || paths.StateDir != tc.roots.State {
				t.Fatalf("roots mismatch: %+v", paths)
			}
			if got, want := paths.DataDir, filepath.Join(home, "srv", "linkari"); got != want {
				t.Fatalf("DataDir=%q want %q", got, want)
			}
			if got, want := paths.QueueDB, filepath.Join(home, "srv", "linkari", "queue.db"); got != want {
				t.Fatalf("QueueDB=%q want %q", got, want)
			}
			if got, want := paths.SnapshotPath, filepath.Join(home, "srv", "linkari", "backups", "latest.db"); got != want {
				t.Fatalf("SnapshotPath=%q want %q", got, want)
			}
			if got, want := paths.TranscriptsDir, filepath.Join(home, "srv", "linkari", "transcripts"); got != want {
				t.Fatalf("TranscriptsDir=%q want %q", got, want)
			}
		})
	}
}

func TestResolveConfigPath_LegacyFallback(t *testing.T) {
	tmp := t.TempDir()
	legacyHome := filepath.Join(tmp, "home")
	withPathResolver(t, fixedResolver{roots: PathRoots{
		Config: filepath.Join(tmp, "Library", "Application Support", "linkari"),
		Data:   filepath.Join(tmp, "Library", "Application Support", "linkari-data"),
		Cache:  filepath.Join(tmp, "Library", "Caches", "linkari"),
		State:  filepath.Join(tmp, "Library", "Application Support", "linkari-state"),
	}})
	t.Setenv("HOME", legacyHome)
	legacyPath := filepath.Join(legacyHome, ".config", "linkari", "config.toml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("[server]\ntoken = \"legacy-token\"\n"), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	res := resolveConfigPath("")
	if !res.Legacy {
		t.Fatalf("expected legacy fallback, got %#v", res)
	}
	if res.Path != legacyPath {
		t.Fatalf("path=%q want %q", res.Path, legacyPath)
	}
	cfg, err := LoadConfig(nil, "")
	if err != nil {
		t.Fatalf("LoadConfig legacy fallback: %v", err)
	}
	if cfg.Server.Token != "legacy-token" {
		t.Fatalf("token=%q want legacy-token", cfg.Server.Token)
	}
	if _, err := os.Stat(filepath.Join(tmp, "Library", "Application Support", "linkari", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("native config should not be created or moved; stat err=%v", err)
	}
}

func TestDoctorReportsLegacyConfigFallback(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	withPathResolver(t, fixedResolver{roots: PathRoots{
		Config: filepath.Join(tmp, "Library", "Application Support", "linkari"),
		Data:   filepath.Join(tmp, "Library", "Application Support", "linkari-data"),
		Cache:  filepath.Join(tmp, "Library", "Caches", "linkari"),
		State:  filepath.Join(tmp, "Library", "Application Support", "linkari-state"),
	}})
	t.Setenv("HOME", home)
	legacyPath := filepath.Join(home, ".config", "linkari", "config.toml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("[server]\ntoken = \"legacy-token\"\n"), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	out, run := newDoctorCmdForTest(t, home, []string{"--json"})
	_ = run()
	var result struct {
		Checks   []doctorCheck `json:"checks"`
		ExitCode int           `json:"exit_code"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("json parse: %v\noutput:\n%s", err, out.String())
	}
	foundWarn := false
	foundConfigOK := false
	for _, c := range result.Checks {
		switch c.Name {
		case "config_toml":
			if c.Status == statusOK {
				foundConfigOK = true
			}
		case "legacy_config_location_detected":
			if c.Status == statusWarn {
				foundWarn = true
			}
		}
	}
	if !foundConfigOK {
		t.Fatalf("expected config_toml ok in output:\n%s", out.String())
	}
	if !foundWarn {
		t.Fatalf("expected legacy config warning in output:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(tmp, "Library", "Application Support", "linkari", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("native config should not be created or moved; stat err=%v", err)
	}
}
