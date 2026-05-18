package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// AgentState represents the outcome of a CWD resolution attempt.
type AgentState int

const (
	AgentActive AgentState = iota // CWD resolved successfully
	AgentIdle                     // Workspace-scoped agent with no active workspace
	AgentMiss                     // Agent not in registry
)

// ResolutionResult holds the output of a CWD resolution.
type ResolutionResult struct {
	CWD           string
	State         AgentState
	Source        string   // "workspace:<name>" | "static" | "none"
	WorkspacePath string   // set when Source starts with "workspace:"
	Warnings      []string // non-fatal issues (e.g., W102 multiple workspace match)
}

// CWDResolver resolves the working directory for a registered agent.
type CWDResolver interface {
	ResolveCWD(agentID string) (ResolutionResult, error)
}

type resolver struct {
	registry      *Registry
	workspaceGlob string
}

// NewResolver creates a CWDResolver that scans workspaceGlob for workspace.yaml files.
func NewResolver(reg *Registry, workspaceGlob string) CWDResolver {
	return &resolver{registry: reg, workspaceGlob: workspaceGlob}
}

// workspaceYAML is the minimal workspace.yaml structure consumed by the resolver.
type workspaceYAML struct {
	Status string `yaml:"status"`
	Repos  []struct {
		Agent string `yaml:"agent"`
		Path  string `yaml:"path"`
	} `yaml:"repos"`
}

func (rs *resolver) ResolveCWD(agentID string) (ResolutionResult, error) {
	agent, ok := rs.registry.LookupAgent(agentID)
	if !ok {
		return ResolutionResult{State: AgentMiss, Source: "none"}, nil
	}

	// Step 1: workspace scan — takes priority over static CWD.
	wsResult, found := rs.scanWorkspaces(agentID)
	if found {
		return wsResult, nil
	}

	// Step 2: static CWD (non-null cwd in org.yaml).
	if agent.CWD != nil && *agent.CWD != "" {
		return ResolutionResult{
			CWD:    *agent.CWD,
			State:  AgentActive,
			Source: "static",
		}, nil
	}

	// Step 3: workspace-scoped agent (cwd=null) with no active workspace → idle.
	return ResolutionResult{State: AgentIdle, Source: "none"}, nil
}

type wsMatch struct {
	cwd           string
	workspaceName string
	modTime       int64
}

func (rs *resolver) scanWorkspaces(agentID string) (ResolutionResult, bool) {
	glob := expandPath(rs.workspaceGlob)
	matches, err := filepath.Glob(glob)
	if err != nil || len(matches) == 0 {
		return ResolutionResult{}, false
	}

	var found []wsMatch
	for _, wsPath := range matches {
		data, err := os.ReadFile(wsPath)
		if err != nil {
			continue
		}
		var ws workspaceYAML
		if err := yaml.Unmarshal(data, &ws); err != nil {
			// W103: malformed workspace.yaml — skip without failing the scan
			continue
		}
		if ws.Status == "closed" || ws.Status == "archived" {
			continue
		}
		for _, repo := range ws.Repos {
			if repo.Agent != agentID {
				continue
			}
			cwd := repo.Path
			if !filepath.IsAbs(cwd) {
				cwd = filepath.Join(filepath.Dir(wsPath), cwd)
			}
			var modTime int64
			if fi, err := os.Stat(wsPath); err == nil {
				modTime = fi.ModTime().UnixNano()
			}
			found = append(found, wsMatch{
				cwd:           cwd,
				workspaceName: filepath.Base(filepath.Dir(wsPath)),
				modTime:       modTime,
			})
		}
	}

	if len(found) == 0 {
		return ResolutionResult{}, false
	}

	result := ResolutionResult{
		State:         AgentActive,
		Source:        fmt.Sprintf("workspace:%s", found[0].workspaceName),
		WorkspacePath: found[0].cwd,
	}

	if len(found) > 1 {
		// W102: multiple matches — use most recent workspace
		sort.Slice(found, func(i, j int) bool { return found[i].modTime > found[j].modTime })
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("W102: Agent '%s' found in %d workspaces — using most recent", agentID, len(found)))
	}

	result.CWD = found[0].cwd
	result.Source = fmt.Sprintf("workspace:%s", found[0].workspaceName)
	result.WorkspacePath = found[0].cwd
	return result, true
}

// expandPath expands ~ and environment variables in a path.
func expandPath(p string) string {
	if len(p) >= 2 && p[0] == '~' && (p[1] == '/' || p[1] == '\\') {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return os.ExpandEnv(p)
}
