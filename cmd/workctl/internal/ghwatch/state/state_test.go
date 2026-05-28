package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	s.SetLastPollTime(now)
	s.SetLastEventID("push", "evt-123")
	s.SetKnownPRs(map[int]PRState{
		42: {State: "open", UpdatedAt: now, Merged: false},
	})
	s.SetKnownWorkflows(map[int64]WFState{
		100: {Status: "completed", Conclusion: "success"},
	})
	s.SetSeenEvents([]string{"a", "b", "c"})

	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload.
	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore reload: %v", err)
	}

	if !s2.LastPollTime().Equal(now) {
		t.Errorf("LastPollTime: got %v, want %v", s2.LastPollTime(), now)
	}
	if id := s2.LastEventID("push"); id != "evt-123" {
		t.Errorf("LastEventID: got %q, want %q", id, "evt-123")
	}
	prs := s2.KnownPRs()
	if pr, ok := prs[42]; !ok || pr.State != "open" {
		t.Errorf("KnownPRs: got %+v, want PR 42 open", prs)
	}
	wfs := s2.KnownWorkflows()
	if wf, ok := wfs[100]; !ok || wf.Conclusion != "success" {
		t.Errorf("KnownWorkflows: got %+v, want run 100 success", wfs)
	}
	seen := s2.SeenEvents()
	if len(seen) != 3 {
		t.Errorf("SeenEvents: got %d items, want 3", len(seen))
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s.SetLastEventID("push", "x")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify no .tmp file left behind.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected .tmp file to not exist after save, err=%v", err)
	}

	// Verify main file exists.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected state file to exist: %v", err)
	}
}

func TestMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore should succeed for missing file: %v", err)
	}
	if !s.LastPollTime().IsZero() {
		t.Errorf("expected zero LastPollTime for new store, got %v", s.LastPollTime())
	}
}

func TestCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore should succeed for corrupt file: %v", err)
	}
	// Should have reset to empty state.
	if !s.LastPollTime().IsZero() {
		t.Errorf("expected zero LastPollTime for corrupt file, got %v", s.LastPollTime())
	}
}

func TestSeenEvents_Cap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Exceed the cap.
	ids := make([]string, maxSeenEvents+500)
	for i := range ids {
		ids[i] = string(rune(i))
	}
	s.SetSeenEvents(ids)

	got := s.SeenEvents()
	if len(got) != maxSeenEvents {
		t.Errorf("expected SeenEvents capped at %d, got %d", maxSeenEvents, len(got))
	}
}
