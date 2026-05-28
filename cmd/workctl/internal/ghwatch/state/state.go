package state

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

const (
	stateVersion  = 1
	maxSeenEvents = 10000
)

// PRState persists the known state of a pull request.
type PRState struct {
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
	Merged    bool      `json:"merged"`
}

// WFState persists the known state of a workflow run.
type WFState struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// StateData is the on-disk JSON schema for ghwatch state.
type StateData struct {
	Version        int               `json:"version"`
	LastPollTime   time.Time         `json:"last_poll_time"`
	LastEventIDs   map[string]string `json:"last_event_ids,omitempty"`
	KnownPRs       map[int]PRState   `json:"known_prs,omitempty"`
	KnownWorkflows map[int64]WFState `json:"known_workflows,omitempty"`
	SeenEvents     []string          `json:"seen_events,omitempty"`
}

// Store provides thread-safe access to ghwatch state with atomic file writes.
type Store struct {
	mu   sync.Mutex
	path string
	data StateData
}

// NewStore loads state from path if it exists, or returns an empty store.
func NewStore(path string) (*Store, error) {
	s := &Store{
		path: path,
		data: StateData{
			Version:        stateVersion,
			LastEventIDs:   make(map[string]string),
			KnownPRs:       make(map[int]PRState),
			KnownWorkflows: make(map[int64]WFState),
		},
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		// Corrupt file — start fresh.
		s.data = StateData{
			Version:        stateVersion,
			LastEventIDs:   make(map[string]string),
			KnownPRs:       make(map[int]PRState),
			KnownWorkflows: make(map[int64]WFState),
		}
		return s, nil
	}
	return s, nil
}

// Save atomically writes state to disk (write tmp, rename).
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// --- Getters / Setters ---

func (s *Store) LastPollTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.LastPollTime
}

func (s *Store) SetLastPollTime(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.LastPollTime = t
}

func (s *Store) LastEventID(poller string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.LastEventIDs[poller]
}

func (s *Store) SetLastEventID(poller, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.LastEventIDs[poller] = id
}

func (s *Store) KnownPRs() map[int]PRState {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(map[int]PRState, len(s.data.KnownPRs))
	for k, v := range s.data.KnownPRs {
		cp[k] = v
	}
	return cp
}

func (s *Store) SetKnownPRs(m map[int]PRState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.KnownPRs = m
}

func (s *Store) KnownWorkflows() map[int64]WFState {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(map[int64]WFState, len(s.data.KnownWorkflows))
	for k, v := range s.data.KnownWorkflows {
		cp[k] = v
	}
	return cp
}

func (s *Store) SetKnownWorkflows(m map[int64]WFState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.KnownWorkflows = m
}

func (s *Store) SeenEvents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]string, len(s.data.SeenEvents))
	copy(cp, s.data.SeenEvents)
	return cp
}

func (s *Store) SetSeenEvents(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(ids) > maxSeenEvents {
		ids = ids[len(ids)-maxSeenEvents:]
	}
	s.data.SeenEvents = ids
}

// Path returns the state file path.
func (s *Store) Path() string {
	return s.path
}
