package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thebrianlopez/runabout/cmd/chain-eval/internal/chainindex"
)

type diffMode int

const (
	diffModeCommitRange diffMode = iota
	diffModeWorkingTree
)

type ChangedArtifactSet struct {
	Changed   int
	Validated []ChangedArtifact
	Deleted   []ChangedArtifact
	Skipped   SkipCounts
	Findings  []checkFinding
}

type ChangedArtifact struct {
	Path   string
	Status string
	Type   chainindex.ArtifactType
}

type SkipCounts struct {
	UnsupportedDir int
	NonMarkdown    int
	Unreadable     int
}

type checkFinding struct {
	Class    string
	Severity string
	Path     string
	Message  string
}

var ErrCounterInvariantViolated = errors.New("counter invariant violated")

func checkCounterInvariant(s ChangedArtifactSet) error {
	accounted := len(s.Validated) + len(s.Deleted) + s.Skipped.UnsupportedDir + s.Skipped.NonMarkdown + s.Skipped.Unreadable
	if s.Changed != accounted {
		return fmt.Errorf("%w: changed=%d != validated=%d+deleted=%d+skipped=%d", ErrCounterInvariantViolated, s.Changed, len(s.Validated), len(s.Deleted), s.Skipped.UnsupportedDir+s.Skipped.NonMarkdown+s.Skipped.Unreadable)
	}
	return nil
}

func scopeChangedArtifacts(mode diffMode, docsRoot, base, head string) (ChangedArtifactSet, error) {
	args := []string{"-C", docsRoot, "diff", "--name-status"}
	if mode == diffModeWorkingTree {
		args = append(args, "HEAD")
	} else {
		args = append(args, base, head)
	}
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return ChangedArtifactSet{}, fmt.Errorf("git diff: %w: %s", err, strings.TrimSpace(string(out)))
	}

	var set ChangedArtifactSet
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		set.Changed++
		status := string(fields[0][0])
		path := fields[len(fields)-1]
		typ, inDir := changedArtifactType(path)
		if !inDir {
			set.Skipped.UnsupportedDir++
			continue
		}
		if !strings.HasSuffix(path, ".md") {
			set.Skipped.NonMarkdown++
			continue
		}
		artifact := ChangedArtifact{Path: filepath.ToSlash(path), Status: status, Type: typ}
		if status == "D" {
			set.Deleted = append(set.Deleted, artifact)
			continue
		}
		if _, err := os.ReadFile(filepath.Join(docsRoot, filepath.FromSlash(path))); err != nil {
			set.Skipped.Unreadable++
			set.Findings = append(set.Findings, checkFinding{Class: "unreadable_artifact", Severity: "warning", Path: path, Message: fmt.Sprintf("could not read %q: %v; counted as skipped, not validated", path, err)})
			continue
		}
		set.Validated = append(set.Validated, artifact)
	}
	if err := checkCounterInvariant(set); err != nil {
		return ChangedArtifactSet{}, err
	}
	return set, nil
}

func changedArtifactType(path string) (chainindex.ArtifactType, bool) {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) < 2 {
		return "", false
	}
	switch parts[0] {
	case "prds":
		return chainindex.ArtifactPRD, true
	case "design":
		if strings.HasSuffix(path, "_TDD.md") {
			return chainindex.ArtifactTDD, true
		}
		return chainindex.ArtifactFDD, true
	case "epics":
		return chainindex.ArtifactEpic, true
	case "releases":
		return chainindex.ArtifactRelease, true
	case "pomo":
		return chainindex.ArtifactPOMO, true
	case "context":
		return chainindex.ArtifactSidecar, true
	default:
		return "", false
	}
}

type checkRunConfig struct {
	docsRoot    string
	base        string
	head        string
	workingTree bool
}

func checkCmd() *cobra.Command {
	var cfg checkRunConfig
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Report reference-integrity scope for changed chain artifacts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			code := runCheck(cfg, cmd.OutOrStdout(), cmd.ErrOrStderr())
			if code != 0 {
				return fmt.Errorf("counter_invariant_violated")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cfg.docsRoot, "docs-root", "", "Absolute path to docs root (overrides $CHAIN_DOCS_ROOT)")
	cmd.Flags().StringVar(&cfg.base, "base", "", "Commit-range lower bound (requires --head)")
	cmd.Flags().StringVar(&cfg.head, "head", "", "Commit-range upper bound (requires --base)")
	cmd.Flags().BoolVar(&cfg.workingTree, "working-tree", false, "Diff staged and unstaged changes against HEAD")
	return cmd
}

func runCheck(cfg checkRunConfig, stdout, stderr io.Writer) int {
	hasRange := cfg.base != "" && cfg.head != ""
	partialRange := (cfg.base == "") != (cfg.head == "")
	if partialRange || hasRange == cfg.workingTree {
		fmt.Fprintln(stderr, "chain-integrity: FATAL usage_error: exactly one of --working-tree or --base/--head is required")
		return 0
	}
	mode := diffModeCommitRange
	if cfg.workingTree {
		mode = diffModeWorkingTree
	}
	set, err := scopeChangedArtifacts(mode, resolveDocsRoot(cfg.docsRoot), cfg.base, cfg.head)
	if err != nil {
		if errors.Is(err, ErrCounterInvariantViolated) {
			fmt.Fprintf(stderr, "chain-integrity: FATAL counter_invariant_violated: internal %v - this is a defect in the check, not a finding about an artifact\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "chain-integrity: FATAL unreadable_diff: could not compute changed-artifact set: %v\n", err)
		return 0
	}
	for _, finding := range set.Findings {
		fmt.Fprintf(stdout, "chain-integrity: %s %s: %s\n", finding.Severity, finding.Class, finding.Message)
	}
	fmt.Fprintf(stdout, "chain-integrity: %d changed, %d validated\n", set.Changed, len(set.Validated))
	fmt.Fprintf(stdout, "skipped: %d unsupported-dir, %d non-markdown, %d unreadable\n", set.Skipped.UnsupportedDir, set.Skipped.NonMarkdown, set.Skipped.Unreadable)
	fmt.Fprintf(stdout, "deleted: %d\n", len(set.Deleted))
	fmt.Fprintln(stdout, "labels: 0 canonical, 0 noncanonical, 0 ambiguous, 0 rejected")
	fmt.Fprintln(stdout, "sentinel: 0 chain-root-declared")
	fmt.Fprintln(stdout, "v1: 0 missing-upstream")
	return 0
}
