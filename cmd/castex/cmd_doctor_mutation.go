package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// RegistryMutationEvent is one detected change in org.yaml.
type RegistryMutationEvent struct {
	EventType  string // registry_agent_added | registry_agent_removed | registry_agent_updated
	AgentID    string // set for added/removed
	Archetype  string // set for added
	AgentCount int    // set for updated
	SHA256     string // new SHA256 for all events
}

// RegistryState is persisted between castex doctor runs for diff detection.
type RegistryState struct {
	SHA256   string   `json:"sha256"`
	AgentIDs []string `json:"agent_ids"`
}

// computeFileSHA256 returns the hex SHA256 of a file's contents.
func computeFileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// loadRegistryState reads the persisted registry state; returns zero value if absent.
func loadRegistryState(path string) (RegistryState, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return RegistryState{}, nil
	}
	if err != nil {
		return RegistryState{}, err
	}
	var s RegistryState
	if err := json.Unmarshal(b, &s); err != nil {
		return RegistryState{}, err
	}
	return s, nil
}

// saveRegistryState writes state to disk, creating parent directories as needed.
func saveRegistryState(path string, state RegistryState) error {
	if err := os.MkdirAll(fileDirOf(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func fileDirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}

// diffRegistryAgents computes mutation events by comparing old and new agent ID sets.
// archetypeOf maps agent ID to its archetype in the new registry.
func diffRegistryAgents(prevIDs []string, currIDs []string, currSHA256 string, archetypeOf map[string]string) []RegistryMutationEvent {
	prevSet := make(map[string]bool, len(prevIDs))
	for _, id := range prevIDs {
		prevSet[id] = true
	}
	currSet := make(map[string]bool, len(currIDs))
	for _, id := range currIDs {
		currSet[id] = true
	}

	var events []RegistryMutationEvent
	for _, id := range currIDs {
		if !prevSet[id] {
			events = append(events, RegistryMutationEvent{
				EventType: "registry_agent_added",
				AgentID:   id,
				Archetype: archetypeOf[id],
				SHA256:    currSHA256,
			})
		}
	}
	for _, id := range prevIDs {
		if !currSet[id] {
			events = append(events, RegistryMutationEvent{
				EventType: "registry_agent_removed",
				AgentID:   id,
				SHA256:    currSHA256,
			})
		}
	}
	// If SHA256 changed but agent set is identical, emit a generic "updated" event.
	if len(events) == 0 {
		events = append(events, RegistryMutationEvent{
			EventType:  "registry_agent_updated",
			AgentCount: len(currIDs),
			SHA256:     currSHA256,
		})
	}
	return events
}

// writeMutationEvents appends registry mutation events to the daily JSONL event bus.
func writeMutationEvents(eventBusDir string, events []RegistryMutationEvent) error {
	if eventBusDir == "" || len(events) == 0 {
		return nil
	}
	if err := os.MkdirAll(eventBusDir, 0o755); err != nil {
		return err
	}
	today := time.Now().UTC().Format("2006-01-02")
	busPath := fmt.Sprintf("%s/%s.jsonl", eventBusDir, today)
	ts := time.Now().UTC().Format("20060102T150405Z")

	f, err := os.OpenFile(busPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, ev := range events {
		meta := map[string]interface{}{}
		switch ev.EventType {
		case "registry_agent_added":
			meta["agent_id"] = ev.AgentID
			meta["archetype"] = ev.Archetype
			meta["org_yaml_sha256"] = ev.SHA256
		case "registry_agent_removed":
			meta["agent_id"] = ev.AgentID
			meta["org_yaml_sha256"] = ev.SHA256
		case "registry_agent_updated":
			meta["agent_count"] = ev.AgentCount
			meta["org_yaml_sha256"] = ev.SHA256
		}
		row := map[string]interface{}{
			"schema_version": "2",
			"timestamp":      ts,
			"layer":          "orchestration",
			"event_type":     ev.EventType,
			"event_class":    "background",
			"command":        "castex doctor",
			"metadata":       meta,
		}
		b, err := json.Marshal(row)
		if err != nil {
			continue
		}
		fmt.Fprintf(f, "%s\n", b)
	}
	return nil
}

// sortedStringSlice returns a sorted copy.
func sortedStringSlice(s []string) []string {
	cp := make([]string, len(s))
	copy(cp, s)
	sort.Strings(cp)
	return cp
}
