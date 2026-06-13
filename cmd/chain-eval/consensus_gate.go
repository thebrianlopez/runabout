package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	ErrConsensusMissing   = errors.New("consensus_gate_missing")
	ErrConsensusStale     = errors.New("consensus_gate_stale")
	ErrFrontmatterInvalid = errors.New("consensus_frontmatter_invalid")
	ErrEventBusUnreadable = errors.New("consensus_event_bus_unreadable")
)

// ConsensusGateCheck checks artifacts declaring consensus_gates.promotion against
// the event bus for approved ConsensusGateResult events.
type ConsensusGateCheck struct {
	EventBusDir string // path to JSONL event bus directory
}

// consensusGateEvent represents a consensus_gate_result event emitted by M3.
type consensusGateEvent struct {
	Type         string `json:"type"`
	ArtifactPath string `json:"artifact_path"`
	ArtifactHash string `json:"artifact_hash"`
	Result       string `json:"result"` // "approved" | "rejected"
	RoundID      string `json:"round_id"`
}

type artifactFrontmatter struct {
	ConsensusGates *consensusGatesField `yaml:"consensus_gates"`
}

type consensusGatesField struct {
	Promotion map[string]interface{} `yaml:"promotion"`
}

// Check reads the artifact at path, parses its frontmatter for consensus_gates.promotion.
// Returns nil if the gate is N/A or passes. Returns a wrapped sentinel error on failure.
func (c *ConsensusGateCheck) Check(artifactPath string) error {
	content, err := os.ReadFile(artifactPath)
	if err != nil {
		return fmt.Errorf("%w: cannot read artifact: %v", ErrFrontmatterInvalid, err)
	}
	fm, err := parseArtifactFrontmatter(content)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrFrontmatterInvalid, err)
	}
	if fm.ConsensusGates == nil || fm.ConsensusGates.Promotion == nil {
		return nil // gate N/A
	}
	currentHash := contentSHA256(content)
	events, err := c.scanEventBus()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEventBusUnreadable, err)
	}
	return matchGateResult(artifactPath, currentHash, events)
}

// parseArtifactFrontmatter extracts and parses YAML between the first --- delimiters.
func parseArtifactFrontmatter(content []byte) (artifactFrontmatter, error) {
	var fm artifactFrontmatter
	s := string(content)
	if !strings.HasPrefix(s, "---") {
		return fm, nil
	}
	after := s[3:]
	if len(after) > 0 && after[0] == '\n' {
		after = after[1:]
	}
	end := strings.Index(after, "\n---")
	if end < 0 {
		return fm, fmt.Errorf("unclosed frontmatter delimiter")
	}
	if err := yaml.Unmarshal([]byte(after[:end]), &fm); err != nil {
		return fm, fmt.Errorf("invalid frontmatter YAML: %v", err)
	}
	return fm, nil
}

// contentSHA256 returns the hex-encoded SHA256 of data.
func contentSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// scanEventBus reads all consensus_gate_result events from JSONL files in EventBusDir.
func (c *ConsensusGateCheck) scanEventBus() ([]consensusGateEvent, error) {
	entries, err := os.ReadDir(c.EventBusDir)
	if err != nil {
		return nil, fmt.Errorf("cannot read event bus at %s: %v", c.EventBusDir, err)
	}
	var results []consensusGateEvent
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		evs, err := readGateEventsFrom(filepath.Join(c.EventBusDir, e.Name()))
		if err != nil {
			return nil, err
		}
		results = append(results, evs...)
	}
	return results, nil
}

// readGateEventsFrom parses a single JSONL file for consensus_gate_result events.
func readGateEventsFrom(path string) ([]consensusGateEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open event file %s: %v", path, err)
	}
	defer f.Close() //nolint:errcheck
	var events []consensusGateEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev consensusGateEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue // skip malformed lines
		}
		if ev.Type == "consensus_gate_result" {
			events = append(events, ev)
		}
	}
	return events, scanner.Err()
}

// matchGateResult checks the event bus for an approved gate result for the artifact
// at its current content hash. Supports both absolute and relative path matching.
func matchGateResult(artifactPath, currentHash string, events []consensusGateEvent) error {
	absPath, _ := filepath.Abs(artifactPath)
	for _, ev := range events {
		evAbs, _ := filepath.Abs(ev.ArtifactPath)
		if evAbs != absPath && ev.ArtifactPath != artifactPath {
			continue
		}
		stored := ev.ArtifactHash
		if stored != currentHash {
			storedShort, currentShort := stored, currentHash
			if len(storedShort) > 8 {
				storedShort = storedShort[:8]
			}
			if len(currentShort) > 8 {
				currentShort = currentShort[:8]
			}
			return fmt.Errorf("%w: artifact modified after consensus (stored %s, current %s)",
				ErrConsensusStale, storedShort, currentShort)
		}
		if ev.Result == "approved" {
			return nil
		}
		return fmt.Errorf("%w: consensus result was %q", ErrConsensusMissing, ev.Result)
	}
	return fmt.Errorf("%w: no approved gate result for %s: run `castex consensus submit %s`",
		ErrConsensusMissing, artifactPath, artifactPath)
}

// consensusEventBusDir returns the event bus directory from CHAIN_EVENT_BUS_DIR
// or the default ~/.automation-metrics/events/.
func consensusEventBusDir() string {
	if dir := os.Getenv("CHAIN_EVENT_BUS_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".automation-metrics", "events")
}

// runConsensusGateChecks scans fixture directories for artifact .md files with
// consensus_gates.promotion and checks each against the event bus. Returns true
// if all pass (or are N/A). Prints failure messages to stderr.
func runConsensusGateChecks(fixturesDir, fixture, eventBusDir string) bool {
	checker := &ConsensusGateCheck{EventBusDir: eventBusDir}
	entries, err := os.ReadDir(fixturesDir)
	if err != nil {
		return true // no fixtures dir, skip gracefully
	}
	allPass := true
	for _, e := range entries {
		if !e.IsDir() || (fixture != "" && e.Name() != fixture) {
			continue
		}
		if err := checkFixtureDir(checker, filepath.Join(fixturesDir, e.Name())); err != nil {
			fmt.Fprintf(os.Stderr, "chain-eval: consensus gate FAIL fixture '%s': %v\n", e.Name(), err)
			allPass = false
		}
	}
	return allPass
}

// checkFixtureDir walks a fixture directory and runs the consensus gate check on
// every .md file. Returns the first gate failure encountered, or walk error.
func checkFixtureDir(checker *ConsensusGateCheck, dir string) error {
	var firstErr error
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		if gateErr := checker.Check(path); gateErr != nil && firstErr == nil {
			firstErr = gateErr
		}
		return nil
	})
	if firstErr != nil {
		return firstErr
	}
	return walkErr
}
