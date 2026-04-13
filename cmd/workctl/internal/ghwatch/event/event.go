package event

import (
	"sync"
	"time"
)

// Kind identifies the type of repository event.
type Kind string

const (
	KindPush     Kind = "push"
	KindPR       Kind = "pr"
	KindWorkflow Kind = "workflow"
)

// Event is the unified event model for all polled activity.
type Event struct {
	ID        string    `json:"id"`
	Kind      Kind      `json:"kind"`
	Repo      string    `json:"repo"`
	Timestamp time.Time `json:"timestamp"`

	Push     *PushDetail     `json:"push,omitempty"`
	PR       *PRDetail       `json:"pr,omitempty"`
	Workflow *WorkflowDetail `json:"workflow,omitempty"`
}

// CommitInfo holds metadata for a single commit in a push event.
type CommitInfo struct {
	SHA      string   `json:"sha"`
	Author   string   `json:"author"`
	Message  string   `json:"message"`
	Added    []string `json:"added,omitempty"`
	Removed  []string `json:"removed,omitempty"`
	Modified []string `json:"modified,omitempty"`
}

// PushDetail holds push-event-specific fields.
type PushDetail struct {
	Branch  string       `json:"branch"`
	HeadSHA string       `json:"head_sha,omitempty"`
	Size    int          `json:"size"`
	Commits []CommitInfo `json:"commits"`
}

// PRDetail holds pull-request-event-specific fields.
type PRDetail struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Author string `json:"author"`
	Action string `json:"action"`
	URL    string `json:"url"`
}

// WorkflowDetail holds workflow-run-specific fields.
type WorkflowDetail struct {
	Name       string        `json:"name"`
	Status     string        `json:"status"`
	Conclusion string        `json:"conclusion,omitempty"`
	Branch     string        `json:"branch"`
	Duration   time.Duration `json:"duration,omitempty"`
	URL        string        `json:"url"`
	RunID      int64         `json:"run_id"`
}

// Deduplicator tracks seen event IDs to prevent duplicate output.
type Deduplicator struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

// NewDeduplicator creates a Deduplicator with the given TTL for seen entries.
func NewDeduplicator(ttl time.Duration) *Deduplicator {
	return &Deduplicator{
		seen: make(map[string]time.Time),
		ttl:  ttl,
	}
}

// IsDuplicate returns true if the event ID has been seen within the TTL window.
func (d *Deduplicator) IsDuplicate(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	ts, ok := d.seen[id]
	if !ok {
		return false
	}
	if time.Since(ts) > d.ttl {
		delete(d.seen, id)
		return false
	}
	return true
}

// Mark records an event ID as seen.
func (d *Deduplicator) Mark(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen[id] = time.Now()
}

// Prune removes entries older than the TTL.
func (d *Deduplicator) Prune() {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for id, ts := range d.seen {
		if now.Sub(ts) > d.ttl {
			delete(d.seen, id)
		}
	}
}

// SeenIDs returns a snapshot of all tracked event IDs (for state persistence).
func (d *Deduplicator) SeenIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make([]string, 0, len(d.seen))
	for id := range d.seen {
		ids = append(ids, id)
	}
	return ids
}

// Seed populates the deduplicator from a saved list of event IDs.
func (d *Deduplicator) Seed(ids []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for _, id := range ids {
		d.seen[id] = now
	}
}

// Len returns the number of tracked entries.
func (d *Deduplicator) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}
