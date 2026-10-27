package main

// Workspace routing registry: label-only MVP.
//
// A WorkspaceRegistry is a compact index of ~/workspaces entries (name +
// one-line scope + tag hints), generated offline by
// core/functions/linkari-workspace-registry.fish and stored at
// ~/.config/linkari/workspaces.yaml. The scoring prompt is enriched with the
// registry so Haiku can assign at most one workspace per scored item. This
// phase is label-only: the assignment is persisted for reporting, nothing is
// dispatched anywhere.
//
// Absent file or Enabled=false is a no-op - the scoring path is unchanged
// (same contract as the [wiki] block).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

// Package-level registry slot. Stored at serve startup when
// [workspaces].enabled=true; nil (zero) means workspace routing is disabled.
var workspaceRegistry atomic.Pointer[WorkspaceRegistry]

// activeWorkspaceRegistry returns the loaded registry or nil when disabled.
func activeWorkspaceRegistry() *WorkspaceRegistry { return workspaceRegistry.Load() }

func setWorkspaceRegistryForTest(reg *WorkspaceRegistry) { workspaceRegistry.Store(reg) }

// WorkspaceEntry is one routed workspace in the registry.
type WorkspaceEntry struct {
	Name  string   `yaml:"name" json:"name"`
	Scope string   `yaml:"scope" json:"scope"`
	Tags  []string `yaml:"tags,omitempty" json:"tags,omitempty"`
}

// WorkspaceRegistry is the on-disk shape of workspaces.yaml.
type WorkspaceRegistry struct {
	SchemaVersion int              `yaml:"schema_version" json:"schema_version"`
	Workspaces    []WorkspaceEntry `yaml:"workspaces" json:"workspaces"`
}

// LoadWorkspaceRegistry reads and parses the registry file. Paths starting
// with ~ are expanded. Returns a wrapped error on read or parse failure -
// callers treat this as non-fatal (scoring continues without workspace
// labels); the doctor check surfaces the detail.
func LoadWorkspaceRegistry(path string) (*WorkspaceRegistry, error) {
	if path == "" {
		return nil, fmt.Errorf("workspace_registry: path is empty")
	}
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("workspace_registry: resolve ~: %w", err)
		}
		path = filepath.Join(home, path[1:])
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("workspace_registry: read %s: %w", path, err)
	}
	var reg WorkspaceRegistry
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("workspace_registry: parse %s: %w", path, err)
	}
	if len(reg.Workspaces) == 0 {
		return nil, fmt.Errorf("workspace_registry: %s contains no workspaces", path)
	}
	// Defense-in-depth: drop entries with empty names so the prompt block and
	// runtime validation never see an unmatchable assignment target.
	kept := reg.Workspaces[:0]
	for _, ws := range reg.Workspaces {
		if strings.TrimSpace(ws.Name) != "" {
			kept = append(kept, ws)
		}
	}
	reg.Workspaces = kept
	if len(reg.Workspaces) == 0 {
		return nil, fmt.Errorf("workspace_registry: %s has no usable entries", path)
	}
	return &reg, nil
}

// HasWorkspace reports whether name is a known registry entry. "none" is the
// explicit unassigned marker emitted by the model and always validates.
func (r *WorkspaceRegistry) HasWorkspace(name string) bool {
	if name == "none" || name == "" {
		return true
	}
	for _, ws := range r.Workspaces {
		if ws.Name == name {
			return true
		}
	}
	return false
}

// MatchTags returns the unique registry workspace whose name or tag set
// contains one of userTags (case-insensitive). Returns ("", false) when zero
// or multiple workspaces match - ambiguity defers to stage-2 model inference.
func (r *WorkspaceRegistry) MatchTags(userTags []string) (string, bool) {
	if r == nil || len(userTags) == 0 {
		return "", false
	}
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	tagSet := make(map[string]struct{}, len(userTags))
	for _, t := range userTags {
		tagSet[norm(t)] = struct{}{}
	}
	found := ""
	for _, ws := range r.Workspaces {
		candidates := make([]string, 0, len(ws.Tags)+1)
		candidates = append(candidates, ws.Name)
		candidates = append(candidates, ws.Tags...)
		for _, c := range candidates {
			if _, ok := tagSet[norm(c)]; ok {
				if found != "" && found != ws.Name {
					return "", false // ambiguous: multiple workspaces match
				}
				found = ws.Name
			}
		}
	}
	return found, found != ""
}
