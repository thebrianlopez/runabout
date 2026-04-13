package api

import (
	"strings"
	"testing"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

func TestParseFishHistory(t *testing.T) {
	const epoch1000 = int64(1000)

	tests := []struct {
		name    string
		input   string
		start   int64
		end     int64
		wantLen int
		check   func(*testing.T, []models.ShellCommand)
	}{
		{
			name:  "basic entry in range",
			input: "- cmd: kubectl get pods\n  when: 1000\n",
			start: 900, end: 1100,
			wantLen: 1,
			check: func(t *testing.T, cmds []models.ShellCommand) {
				c := cmds[0]
				if c.Cmd != "kubectl get pods" {
					t.Errorf("Cmd = %q, want %q", c.Cmd, "kubectl get pods")
				}
				if c.Binary != "kubectl" {
					t.Errorf("Binary = %q, want kubectl", c.Binary)
				}
				if c.Category != CategoryKubernetes {
					t.Errorf("Category = %q, want %q", c.Category, CategoryKubernetes)
				}
				if !c.IsInfra {
					t.Error("IsInfra should be true for kubectl")
				}
				if c.IsDeploy {
					t.Error("IsDeploy should be false for kubectl get")
				}
			},
		},
		{
			name:  "entry filtered out (before range)",
			input: "- cmd: ls\n  when: 500\n",
			start: 900, end: 1100,
			wantLen: 0,
		},
		{
			name:  "entry filtered out (after range)",
			input: "- cmd: ls\n  when: 1200\n",
			start: 900, end: 1100,
			wantLen: 0,
		},
		{
			name:  "boundary: entry exactly at start",
			input: "- cmd: ls\n  when: 900\n",
			start: 900, end: 1100,
			wantLen: 1,
		},
		{
			name:  "boundary: entry exactly at end",
			input: "- cmd: ls\n  when: 1100\n",
			start: 900, end: 1100,
			wantLen: 1,
		},
		{
			name:  "entry with paths",
			input: "- cmd: vi ~/config.fish\n  when: 1000\n  paths:\n    - ~/config.fish\n",
			start: 900, end: 1100,
			wantLen: 1,
			check: func(t *testing.T, cmds []models.ShellCommand) {
				if len(cmds[0].Paths) != 1 || cmds[0].Paths[0] != "~/config.fish" {
					t.Errorf("Paths = %v, want [~/config.fish]", cmds[0].Paths)
				}
			},
		},
		{
			name:  "entry with multiple paths",
			input: "- cmd: diff a.go b.go\n  when: 1000\n  paths:\n    - a.go\n    - b.go\n",
			start: 900, end: 1100,
			wantLen: 1,
			check: func(t *testing.T, cmds []models.ShellCommand) {
				if len(cmds[0].Paths) != 2 {
					t.Errorf("len(Paths) = %d, want 2", len(cmds[0].Paths))
				}
			},
		},
		{
			name:  "entry without paths field",
			input: "- cmd: ls -la\n  when: 1000\n",
			start: 900, end: 1100,
			wantLen: 1,
			check: func(t *testing.T, cmds []models.ShellCommand) {
				if len(cmds[0].Paths) != 0 {
					t.Errorf("Paths should be empty, got %v", cmds[0].Paths)
				}
			},
		},
		{
			name:  "multiple entries",
			input: "- cmd: git status\n  when: 1000\n- cmd: git push\n  when: 1050\n",
			start: 900, end: 1100,
			wantLen: 2,
		},
		{
			name:  "multiple entries — only some in range",
			input: "- cmd: ls\n  when: 500\n- cmd: git status\n  when: 1000\n- cmd: ls\n  when: 2000\n",
			start: 900, end: 1100,
			wantLen: 1,
		},
		{
			name:  "escape: \\n becomes newline",
			input: "- cmd: echo hello\\nworld\n  when: 1000\n",
			start: 900, end: 1100,
			wantLen: 1,
			check: func(t *testing.T, cmds []models.ShellCommand) {
				if cmds[0].Cmd != "echo hello\nworld" {
					t.Errorf("Cmd = %q, want %q", cmds[0].Cmd, "echo hello\nworld")
				}
			},
		},
		{
			name:  "escape: \\\\ becomes single backslash",
			input: "- cmd: echo path\\\\to\\\\file\n  when: 1000\n",
			start: 900, end: 1100,
			wantLen: 1,
			check: func(t *testing.T, cmds []models.ShellCommand) {
				want := `echo path\to\file`
				if cmds[0].Cmd != want {
					t.Errorf("Cmd = %q, want %q", cmds[0].Cmd, want)
				}
			},
		},
		{
			name:  "missing when treated as epoch 0",
			input: "- cmd: ls\n",
			start: 0, end: 100,
			wantLen: 1,
		},
		{
			name:  "deploy command flagged",
			input: "- cmd: kubectl apply -f manifest.yaml\n  when: 1000\n",
			start: 900, end: 1100,
			wantLen: 1,
			check: func(t *testing.T, cmds []models.ShellCommand) {
				if !cmds[0].IsDeploy {
					t.Error("IsDeploy should be true for kubectl apply")
				}
			},
		},
		{
			name:  "sensitive value redacted",
			input: "- cmd: export TOKEN=secret123\n  when: 1000\n",
			start: 900, end: 1100,
			wantLen: 1,
			check: func(t *testing.T, cmds []models.ShellCommand) {
				if strings.Contains(cmds[0].Cmd, "secret123") {
					t.Errorf("sensitive value should be redacted in Cmd: %q", cmds[0].Cmd)
				}
				if !strings.Contains(cmds[0].Cmd, "[REDACTED]") {
					t.Errorf("expected [REDACTED] placeholder in Cmd: %q", cmds[0].Cmd)
				}
			},
		},
		{
			name:  "env var prefix skipped for binary extraction",
			input: "- cmd: AWS_PROFILE=prod terraform plan\n  when: 1000\n",
			start: 900, end: 1100,
			wantLen: 1,
			check: func(t *testing.T, cmds []models.ShellCommand) {
				if cmds[0].Binary != "terraform" {
					t.Errorf("Binary = %q, want terraform", cmds[0].Binary)
				}
				if cmds[0].Category != CategoryTerraform {
					t.Errorf("Category = %q, want %q", cmds[0].Category, CategoryTerraform)
				}
			},
		},
		{
			name:  "timestamp set correctly",
			input: "- cmd: ls\n  when: 1000\n",
			start: 900, end: 1100,
			wantLen: 1,
			check: func(t *testing.T, cmds []models.ShellCommand) {
				if cmds[0].Timestamp.Unix() != epoch1000 {
					t.Errorf("Timestamp.Unix() = %d, want %d", cmds[0].Timestamp.Unix(), epoch1000)
				}
			},
		},
		{
			name:  "empty input",
			input: "",
			start: 0, end: 9999999,
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmds, err := parseFishHistory(strings.NewReader(tt.input), tt.start, tt.end)
			if err != nil {
				t.Fatalf("parseFishHistory error: %v", err)
			}
			if len(cmds) != tt.wantLen {
				t.Errorf("len(cmds) = %d, want %d", len(cmds), tt.wantLen)
				return
			}
			if tt.check != nil && len(cmds) > 0 {
				tt.check(t, cmds)
			}
		})
	}
}

func TestGetCommands_FileAbsent(t *testing.T) {
	c := newFishHistoryClientAt("/nonexistent/path/fish_history")
	cmds, err := c.GetCommands("2026-01-01", "2026-01-07")
	if err != nil {
		t.Errorf("GetCommands should not error on missing file, got: %v", err)
	}
	if len(cmds) != 0 {
		t.Errorf("GetCommands should return empty slice on missing file, got %d entries", len(cmds))
	}
}

func TestUnescapeFishCmd(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`no escapes`, `no escapes`},
		{`echo hello\nworld`, "echo hello\nworld"},
		{`echo path\\to\\file`, `echo path\to\file`},
		{`trailing backslash\`, `trailing backslash\`},
		{`\x unknown escape`, `\x unknown escape`}, // unknown: pass through as-is
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := unescapeFishCmd(tt.input); got != tt.want {
				t.Errorf("unescapeFishCmd(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
