package main

import (
	"testing"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/workspace"
)

func TestIsValidJiraKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"ISRE-1234", true},
		{"SR-1", true},
		{"ABC-99999", true},
		{"isre-1234", false},
		{"ISRE", false},
		{"-1234", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := workspace.IsValidJiraKey(tt.key); got != tt.want {
				t.Errorf("IsValidJiraKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestWorkspaceCmd_Structure(t *testing.T) {
	cmd := workspaceCmd()

	if cmd.Use != "workspace" {
		t.Errorf("Use = %q, want %q", cmd.Use, "workspace")
	}

	if len(cmd.Aliases) == 0 || cmd.Aliases[0] != "ws" {
		t.Errorf("Aliases = %v, want [ws]", cmd.Aliases)
	}

	// Check that init subcommand exists
	initCmd, _, err := cmd.Find([]string{"init"})
	if err != nil {
		t.Fatalf("init subcommand not found: %v", err)
	}
	if initCmd.Use != "init <JIRA-KEY | ISSUE-NUMBER>" {
		t.Errorf("init Use = %q, want %q", initCmd.Use, "init <JIRA-KEY | ISSUE-NUMBER>")
	}

	// Check flags exist
	for _, flag := range []string{"dry-run", "repos", "force", "verbose", "github-repo"} {
		if initCmd.Flags().Lookup(flag) == nil {
			t.Errorf("missing flag: %s", flag)
		}
	}

	// Check shorthand flags
	for _, pair := range []struct{ long, short string }{
		{"dry-run", "n"},
		{"repos", "r"},
		{"force", "f"},
		{"verbose", "v"},
		{"github-repo", "R"},
	} {
		f := initCmd.Flags().Lookup(pair.long)
		if f == nil {
			t.Errorf("missing flag: %s", pair.long)
			continue
		}
		if f.Shorthand != pair.short {
			t.Errorf("flag %s shorthand = %q, want %q", pair.long, f.Shorthand, pair.short)
		}
	}
}
