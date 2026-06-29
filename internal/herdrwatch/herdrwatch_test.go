package herdrwatch

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type fakeExec struct {
	out []byte
	err error
}

func (f fakeExec) Run(ctx context.Context, command string) ([]byte, error) { return f.out, f.err }

func TestNormalizeAgentList(t *testing.T) {
	snap, err := NormalizeAgentList([]byte(`{"result":{"agents":[{"pane":"w1:p1","agent_status":"blocked"},{"id":"w1:p2","status":"done"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Agents["w1:p1"].State != StatusBlocked || snap.Agents["w1:p2"].State != StatusDone {
		t.Fatalf("bad snap: %#v", snap)
	}
}

func TestDiffNotifyOnlyOnNotifyStatuses(t *testing.T) {
	prev := Snapshot{Agents: map[string]Agent{"p1": {Pane: "p1", State: StatusReady}}}
	curr := Snapshot{Agents: map[string]Agent{"p1": {Pane: "p1", State: StatusBlocked}, "p2": {Pane: "p2", State: StatusReady}}}
	trs := Diff(prev, curr, time.Unix(1, 0))
	if len(trs) != 1 || trs[0].Pane != "p1" || trs[0].To != StatusBlocked {
		t.Fatalf("bad trs: %#v", trs)
	}
}

func TestStateStoreRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	s := StateStore{Path: p}
	snap := Snapshot{Agents: map[string]Agent{"p1": {Pane: "p1", State: StatusDone}}}
	if err := s.Save(snap); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Agents["p1"].State != StatusDone {
		t.Fatalf("got %#v", got)
	}
}

func TestPoller(t *testing.T) {
	p := StateStore{Path: filepath.Join(t.TempDir(), "state.json")}
	poller := Poller{Exec: fakeExec{out: []byte(`{"result":{"agents":[{"pane":"p1","agent_status":"blocked"}]}}`)}, State: p, Command: "herdr agent list", Now: func() time.Time { return time.Unix(10, 0) }}
	res, err := poller.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Transitions) != 1 {
		t.Fatalf("expected transition")
	}
}

func TestLoadMissingState(t *testing.T) {
	_, err := (StateStore{Path: filepath.Join(t.TempDir(), "missing.json")}).Load()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSaveRequiresPath(t *testing.T) {
	if err := (StateStore{}).Save(Snapshot{}); err == nil {
		t.Fatal("expected error")
	}
	_ = errors.New("")
}
