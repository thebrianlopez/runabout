package herdrwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type Status string

const (
	StatusUnknown Status = ""
	StatusBlocked Status = "blocked"
	StatusDone    Status = "done"
	StatusReady   Status = "ready"
)

type Agent struct {
	Pane  string `json:"pane"`
	State Status `json:"state"`
}

type Snapshot struct {
	Agents map[string]Agent `json:"agents"`
}

type Transition struct {
	Pane       string    `json:"pane"`
	From       Status    `json:"from"`
	To         Status    `json:"to"`
	ObservedAt time.Time `json:"observed_at"`
}

type PollResult struct {
	Snapshot    Snapshot
	Transitions []Transition
}

var notifyStatuses = map[Status]bool{StatusBlocked: true, StatusDone: true}

func NormalizeAgentList(data []byte) (Snapshot, error) {
	var raw struct {
		Result struct {
			Agents []struct {
				Pane        string `json:"pane"`
				PaneID      string `json:"pane_id"`
				ID          string `json:"id"`
				AgentStatus string `json:"agent_status"`
				Status      string `json:"status"`
			} `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Agents: map[string]Agent{}}
	for _, a := range raw.Result.Agents {
		pane := firstNonEmpty(a.Pane, a.PaneID, a.ID)
		if pane == "" {
			continue
		}
		st := Status(firstNonEmpty(a.AgentStatus, a.Status))
		snap.Agents[pane] = Agent{Pane: pane, State: st}
	}
	return snap, nil
}

func Diff(previous, current Snapshot, observedAt time.Time) []Transition {
	var panes []string
	seen := map[string]struct{}{}
	for pane := range previous.Agents {
		seen[pane] = struct{}{}
	}
	for pane := range current.Agents {
		seen[pane] = struct{}{}
	}
	for pane := range seen {
		panes = append(panes, pane)
	}
	sort.Strings(panes)
	var out []Transition
	for _, pane := range panes {
		from := previous.Agents[pane].State
		to := current.Agents[pane].State
		if from == to {
			continue
		}
		if !notifyStatuses[to] {
			continue
		}
		out = append(out, Transition{Pane: pane, From: from, To: to, ObservedAt: observedAt})
	}
	return out
}

type StateStore struct{ Path string }

func (s StateStore) Load() (Snapshot, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{Agents: map[string]Agent{}}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return Snapshot{}, err
	}
	if snap.Agents == nil {
		snap.Agents = map[string]Agent{}
	}
	return snap, nil
}

func (s StateStore) Save(snap Snapshot) error {
	if s.Path == "" {
		return fmt.Errorf("state path is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, b, 0o600)
}

type Executor interface {
	Run(ctx context.Context, command string) ([]byte, error)
}

type Poller struct {
	Exec    Executor
	State   StateStore
	Command string
	Now     func() time.Time
}

func (p Poller) Poll(ctx context.Context) (PollResult, error) {
	if p.Now == nil {
		p.Now = time.Now
	}
	prev, err := p.State.Load()
	if err != nil {
		return PollResult{}, err
	}
	b, err := p.Exec.Run(ctx, p.Command)
	if err != nil {
		return PollResult{}, err
	}
	curr, err := NormalizeAgentList(b)
	if err != nil {
		return PollResult{}, err
	}
	trs := Diff(prev, curr, p.Now())
	if err := p.State.Save(curr); err != nil {
		return PollResult{}, err
	}
	return PollResult{Snapshot: curr, Transitions: trs}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
