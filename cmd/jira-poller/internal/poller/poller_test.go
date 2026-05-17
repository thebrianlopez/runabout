package poller_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/config"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/jiraclient"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/poller"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/publisher"
	"github.com/thebrianlopez/runabout/cmd/jira-poller/internal/types"
)

// ──────────────────────────────────────────────────────────────────────────────
// Mock implementations
// ──────────────────────────────────────────────────────────────────────────────

// mockJiraClient implements jiraclient.Client for tests.
type mockJiraClient struct {
	mu      sync.Mutex
	results []jiraclient.SearchResult
	err     error
	idx     int
	calls   []jiraclient.SearchRequest
	// block causes SearchTransitions to wait for release before returning.
	block   chan struct{}
}

func (m *mockJiraClient) SearchTransitions(_ context.Context, req jiraclient.SearchRequest) (*jiraclient.SearchResult, error) {
	if m.block != nil {
		<-m.block
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)
	if m.err != nil {
		return nil, m.err
	}
	if m.idx >= len(m.results) {
		return &jiraclient.SearchResult{}, nil
	}
	r := m.results[m.idx]
	m.idx++
	return &r, nil
}

// mockDedupeStore implements dedupe.DedupeStore for tests.
type mockDedupeStore struct {
	mu     sync.Mutex
	isNew  bool
	err    error
	calls  []string // event IDs passed to Mark
}

func (m *mockDedupeStore) Mark(_ context.Context, eventID string, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, eventID)
	return m.isNew, m.err
}

// mockPublisher implements publisher.Publisher for tests.
type mockPublisher struct {
	mu       sync.Mutex
	result   publisher.PublishResult
	err      error
	calls    [][]types.TransitionEvent
}

func (m *mockPublisher) Publish(_ context.Context, events []types.TransitionEvent) (publisher.PublishResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]types.TransitionEvent, len(events))
	copy(cp, events)
	m.calls = append(m.calls, cp)
	if m.err != nil {
		return publisher.PublishResult{}, m.err
	}
	// Default: succeed for all events unless pre-configured.
	if m.result.Succeeded == nil && m.result.Failed == nil {
		ids := make([]string, len(events))
		for i, ev := range events {
			ids[i] = ev.ChangelogID
		}
		return publisher.PublishResult{Succeeded: ids}, nil
	}
	return m.result, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func testConfig() config.Config {
	return config.Config{
		JiraBaseURL:    "https://jira.example.com",
		JiraProjects:   []string{"INFRA"},
		PollInterval:   time.Hour, // effectively disabled in unit tests
		LookbackWindow: 2 * time.Hour,
		LocalDev:       true,
		LogFormat:      "json",
		CredentialTTL:  6 * time.Hour,
	}
}

func fixedNow(ts int64) func() time.Time {
	return func() time.Time { return time.Unix(ts, 0) }
}

func makeIssue(key string, entries []jiraclient.ChangelogEntry) jiraclient.Issue {
	return jiraclient.Issue{
		Key:       key,
		Summary:   "Test issue",
		IssueType: "Bug",
		Self:      "https://jira.example.com/" + key,
		Labels:    []string{},
		Changelog: entries,
	}
}

func makeEntry(historyID string, created time.Time, from, to string) jiraclient.ChangelogEntry {
	return jiraclient.ChangelogEntry{
		HistoryID:  historyID,
		Created:    created,
		Author:     jiraclient.User{AccountID: "u1", DisplayName: "Alice"},
		FromStatus: from,
		ToStatus:   to,
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(devNull{}, nil))
}

type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }

// ──────────────────────────────────────────────────────────────────────────────
// Contract tests
// ──────────────────────────────────────────────────────────────────────────────

// CT-4: Poll cycle extracts only status-field transitions inside the lookback
// window. Non-status entries and out-of-window entries are excluded.
func TestPollOnce_CT4_FiltersLookbackAndStatusOnly(t *testing.T) {
	const epoch int64 = 10000
	cfg := testConfig()
	cfg.LookbackWindow = 5 * time.Minute

	// Entry inside window.
	inWindow := makeEntry("h1", time.Unix(epoch-120, 0), "To Do", "In Progress")
	// Entry outside window.
	outOfWindow := makeEntry("h2", time.Unix(epoch-400, 0), "Backlog", "To Do")

	jira := &mockJiraClient{
		results: []jiraclient.SearchResult{
			{Issues: []jiraclient.Issue{makeIssue("INFRA-1", []jiraclient.ChangelogEntry{inWindow, outOfWindow})}},
		},
	}
	store := &mockDedupeStore{isNew: true}
	pub := &mockPublisher{}

	p := poller.New(cfg, jira, store, pub, fixedNow(epoch), discardLogger())
	p.PollOnce(context.Background())

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.calls) != 1 {
		t.Fatalf("want 1 Publish call (1 in-window event), got %d", len(pub.calls))
	}
	if len(pub.calls[0]) != 1 {
		t.Fatalf("want 1 event in call, got %d", len(pub.calls[0]))
	}
	if pub.calls[0][0].ChangelogID != "INFRA-1:h1" {
		t.Errorf("ChangelogID = %q, want INFRA-1:h1", pub.calls[0][0].ChangelogID)
	}
}

// CT-5: Null fromStatus (empty string from jiraclient) → TransitionEvent.FromStatus == "".
func TestPollOnce_CT5_NullFromStatus(t *testing.T) {
	const epoch int64 = 10000
	cfg := testConfig()
	cfg.LookbackWindow = time.Hour

	entry := makeEntry("h1", time.Unix(epoch-60, 0), "", "In Progress")

	jira := &mockJiraClient{
		results: []jiraclient.SearchResult{
			{Issues: []jiraclient.Issue{makeIssue("INFRA-1", []jiraclient.ChangelogEntry{entry})}},
		},
	}
	pub := &mockPublisher{}

	p := poller.New(cfg, jira, &mockDedupeStore{isNew: true}, pub, fixedNow(epoch), discardLogger())
	p.PollOnce(context.Background())

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.calls) == 0 || len(pub.calls[0]) == 0 {
		t.Fatal("expected Publish to be called")
	}
	if pub.calls[0][0].FromStatus != "" {
		t.Errorf("FromStatus = %q, want empty string", pub.calls[0][0].FromStatus)
	}
}

// CT-6: Missing assignee → TransitionEvent.Assignee == nil.
func TestPollOnce_CT6_NilAssignee(t *testing.T) {
	const epoch int64 = 10000
	cfg := testConfig()
	cfg.LookbackWindow = time.Hour

	issue := makeIssue("INFRA-1", []jiraclient.ChangelogEntry{
		makeEntry("h1", time.Unix(epoch-60, 0), "To Do", "In Progress"),
	})
	issue.Assignee = nil // explicit

	jira := &mockJiraClient{results: []jiraclient.SearchResult{{Issues: []jiraclient.Issue{issue}}}}
	pub := &mockPublisher{}

	p := poller.New(cfg, jira, &mockDedupeStore{isNew: true}, pub, fixedNow(epoch), discardLogger())
	p.PollOnce(context.Background())

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.calls) == 0 || len(pub.calls[0]) == 0 {
		t.Fatal("expected Publish to be called")
	}
	if pub.calls[0][0].Assignee != nil {
		t.Errorf("Assignee = %v, want nil", pub.calls[0][0].Assignee)
	}
}

// CT-7: ChangelogID format is "{IssueKey}:{HistoryID}".
func TestPollOnce_CT7_ChangelogIDFormat(t *testing.T) {
	const epoch int64 = 10000
	cfg := testConfig()
	cfg.LookbackWindow = time.Hour

	jira := &mockJiraClient{results: []jiraclient.SearchResult{{
		Issues: []jiraclient.Issue{makeIssue("INFRA-1234", []jiraclient.ChangelogEntry{
			makeEntry("10234", time.Unix(epoch-60, 0), "To Do", "Done"),
		})},
	}}}
	pub := &mockPublisher{}

	p := poller.New(cfg, jira, &mockDedupeStore{isNew: true}, pub, fixedNow(epoch), discardLogger())
	p.PollOnce(context.Background())

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.calls) == 0 || len(pub.calls[0]) == 0 {
		t.Fatal("expected Publish to be called")
	}
	if pub.calls[0][0].ChangelogID != "INFRA-1234:10234" {
		t.Errorf("ChangelogID = %q, want INFRA-1234:10234", pub.calls[0][0].ChangelogID)
	}
}

// CT-8: ProjectKey extracted from IssueKey by splitting on "-".
func TestPollOnce_CT8_ProjectKeyExtraction(t *testing.T) {
	const epoch int64 = 10000
	cfg := testConfig()
	cfg.LookbackWindow = time.Hour

	jira := &mockJiraClient{results: []jiraclient.SearchResult{{
		Issues: []jiraclient.Issue{makeIssue("PLAT-99", []jiraclient.ChangelogEntry{
			makeEntry("h1", time.Unix(epoch-60, 0), "To Do", "Done"),
		})},
	}}}
	pub := &mockPublisher{}

	p := poller.New(cfg, jira, &mockDedupeStore{isNew: true}, pub, fixedNow(epoch), discardLogger())
	p.PollOnce(context.Background())

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.calls) == 0 || len(pub.calls[0]) == 0 {
		t.Fatal("expected Publish to be called")
	}
	if pub.calls[0][0].ProjectKey != "PLAT" {
		t.Errorf("ProjectKey = %q, want PLAT", pub.calls[0][0].ProjectKey)
	}
}

// CT-9: Publish-then-mark ordering: Mark NOT called if Publish returns fatal error.
func TestPollOnce_CT9_MarkNotCalledOnPublishError(t *testing.T) {
	const epoch int64 = 10000
	cfg := testConfig()
	cfg.LookbackWindow = time.Hour

	jira := &mockJiraClient{results: []jiraclient.SearchResult{{
		Issues: []jiraclient.Issue{makeIssue("INFRA-1", []jiraclient.ChangelogEntry{
			makeEntry("h1", time.Unix(epoch-60, 0), "To Do", "In Progress"),
		})},
	}}}
	store := &mockDedupeStore{isNew: true}
	pub := &mockPublisher{err: publisher.ErrOutboxWrite}

	p := poller.New(cfg, jira, store, pub, fixedNow(epoch), discardLogger())
	p.PollOnce(context.Background())

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.calls) != 0 {
		t.Errorf("Mark call count = %d, want 0 (publish failed)", len(store.calls))
	}
}

// CT-10: Duplicate event (already in outbox → result.Failed) → Mark NOT called.
// Events that the Publisher reports as Failed (UNIQUE constraint in outbox) are
// duplicates from a previous cycle; they must not be marked in the dedupe store.
func TestPollOnce_CT10_DuplicateInOutbox_MarkNotCalled(t *testing.T) {
	const epoch int64 = 10000
	cfg := testConfig()
	cfg.LookbackWindow = time.Hour

	jira := &mockJiraClient{results: []jiraclient.SearchResult{{
		Issues: []jiraclient.Issue{makeIssue("INFRA-1", []jiraclient.ChangelogEntry{
			makeEntry("h1", time.Unix(epoch-60, 0), "To Do", "In Progress"),
		})},
	}}}
	store := &mockDedupeStore{isNew: false} // would return false if called
	// Publisher returns event in Failed (UNIQUE constraint duplicate).
	pub := &mockPublisher{
		result: publisher.PublishResult{
			Failed: []publisher.FailedEvent{{ChangelogID: "INFRA-1:h1", ErrorMessage: "duplicate"}},
		},
	}

	p := poller.New(cfg, jira, store, pub, fixedNow(epoch), discardLogger())
	p.PollOnce(context.Background())

	pub.mu.Lock()
	pubCalls := len(pub.calls)
	pub.mu.Unlock()
	if pubCalls == 0 {
		t.Fatal("expected Publish to be called")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.calls) != 0 {
		t.Errorf("Mark call count = %d, want 0 (duplicate event in outbox)", len(store.calls))
	}
}

// CT-11: Concurrent poll prevention: second PollOnce while first is running →
// returns ErrPollOverlap.
func TestPollOnce_CT11_OverlapPrevention(t *testing.T) {
	cfg := testConfig()

	release := make(chan struct{})
	jira := &mockJiraClient{
		block:   release,
		results: []jiraclient.SearchResult{{Issues: []jiraclient.Issue{}}},
	}

	p := poller.New(cfg, jira, &mockDedupeStore{isNew: true}, &mockPublisher{}, time.Now, discardLogger())

	// Start first poll cycle in background (will block on jira mock).
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.PollOnce(context.Background())
	}()

	// Give the goroutine time to acquire the mutex.
	time.Sleep(20 * time.Millisecond)

	// Second call should get ErrPollOverlap immediately.
	err := p.PollOnce(context.Background())
	if !errors.Is(err, poller.ErrPollOverlap) {
		t.Errorf("second PollOnce err = %v, want ErrPollOverlap", err)
	}

	// Unblock first poll and wait for completion.
	close(release)
	<-done
}

// CT-12: Run respects context cancellation but finishes the in-flight poll first.
func TestRun_CT12_GracefulShutdown(t *testing.T) {
	cfg := testConfig()
	cfg.PollInterval = 10 * time.Millisecond // fire quickly

	release := make(chan struct{})
	completed := false
	jira := &mockJiraClient{
		block: release,
		results: []jiraclient.SearchResult{{Issues: []jiraclient.Issue{}}},
	}

	var wg sync.WaitGroup
	p := poller.New(cfg, jira, &mockDedupeStore{isNew: true}, &mockPublisher{}, time.Now, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())

	wg.Add(1)
	go func() {
		defer wg.Done()
		p.Run(ctx)
		completed = true
	}()

	// Wait for the first tick to fire and the poll to start blocking.
	time.Sleep(50 * time.Millisecond)

	// Cancel the context (simulate SIGTERM).
	cancel()

	// Run should not return yet — poll is still in progress.
	time.Sleep(20 * time.Millisecond)
	if completed {
		t.Error("Run returned before poll completed — should wait for in-flight cycle")
	}

	// Unblock the poll. Run should now return.
	close(release)
	wg.Wait()

	if !completed {
		t.Error("Run did not complete after unblocking poll")
	}
}

// CT-13: LastPollTime updated after a successful cycle; zero before first cycle.
func TestPollOnce_CT13_LastPollTimeUpdated(t *testing.T) {
	const epoch int64 = 10000
	cfg := testConfig()
	cfg.LookbackWindow = time.Hour

	jira := &mockJiraClient{results: []jiraclient.SearchResult{{Issues: []jiraclient.Issue{}}}}
	p := poller.New(cfg, jira, &mockDedupeStore{isNew: true}, &mockPublisher{}, fixedNow(epoch), discardLogger())

	if !p.LastPollTime().IsZero() {
		t.Error("LastPollTime should be zero before first cycle")
	}

	p.PollOnce(context.Background())

	got := p.LastPollTime()
	if got.IsZero() {
		t.Error("LastPollTime should be non-zero after successful cycle")
	}
	if got.Unix() != epoch {
		t.Errorf("LastPollTime = %v, want epoch %d", got.Unix(), epoch)
	}
}

// CT-14: Jira error → cycle skipped; LastPollTime not updated.
func TestPollOnce_CT14_JiraError_LastPollTimeUnchanged(t *testing.T) {
	cfg := testConfig()

	jira := &mockJiraClient{err: jiraclient.ErrUpstream}
	p := poller.New(cfg, jira, &mockDedupeStore{isNew: true}, &mockPublisher{}, time.Now, discardLogger())

	p.PollOnce(context.Background())

	if !p.LastPollTime().IsZero() {
		t.Error("LastPollTime should remain zero after a failed cycle")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Behavioural tests
// ──────────────────────────────────────────────────────────────────────────────

// BT-1: Multiple transitions on one issue in one poll → separate events.
func TestPollOnce_BT1_MultipleTransitionsOneIssue(t *testing.T) {
	const epoch int64 = 10000
	cfg := testConfig()
	cfg.LookbackWindow = time.Hour

	entries := []jiraclient.ChangelogEntry{
		makeEntry("h1", time.Unix(epoch-300, 0), "To Do", "In Progress"),
		makeEntry("h2", time.Unix(epoch-200, 0), "In Progress", "Review"),
		makeEntry("h3", time.Unix(epoch-100, 0), "Review", "Done"),
	}
	jira := &mockJiraClient{results: []jiraclient.SearchResult{{
		Issues: []jiraclient.Issue{makeIssue("INFRA-1", entries)},
	}}}
	pub := &mockPublisher{}

	p := poller.New(cfg, jira, &mockDedupeStore{isNew: true}, pub, fixedNow(epoch), discardLogger())
	p.PollOnce(context.Background())

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.calls) != 3 {
		t.Errorf("want 3 Publish calls, got %d", len(pub.calls))
	}
}

// BT-2: Transitions outside lookback window are excluded entirely.
func TestPollOnce_BT2_OldTransitionsExcluded(t *testing.T) {
	const epoch int64 = 10000
	cfg := testConfig()
	cfg.LookbackWindow = 5 * time.Minute

	// Entry created just after the cutoff (outside window).
	outsideEntry := makeEntry("h1", time.Unix(epoch-int64(5*time.Minute.Seconds())-1, 0), "To Do", "Done")

	jira := &mockJiraClient{results: []jiraclient.SearchResult{{
		Issues: []jiraclient.Issue{makeIssue("INFRA-1", []jiraclient.ChangelogEntry{outsideEntry})},
	}}}
	pub := &mockPublisher{}

	p := poller.New(cfg, jira, &mockDedupeStore{isNew: true}, pub, fixedNow(epoch), discardLogger())
	p.PollOnce(context.Background())

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.calls) != 0 {
		t.Errorf("want 0 Publish calls (entry outside window), got %d", len(pub.calls))
	}
}

// BT-3: Pagination — poller loops until NextToken is empty.
func TestPollOnce_BT3_PaginationLoops(t *testing.T) {
	const epoch int64 = 10000
	cfg := testConfig()
	cfg.LookbackWindow = time.Hour

	entry := makeEntry("h1", time.Unix(epoch-60, 0), "To Do", "Done")
	jira := &mockJiraClient{results: []jiraclient.SearchResult{
		{Issues: []jiraclient.Issue{makeIssue("INFRA-1", []jiraclient.ChangelogEntry{entry})}, NextToken: "page2"},
		{Issues: []jiraclient.Issue{makeIssue("INFRA-2", []jiraclient.ChangelogEntry{entry})}, NextToken: ""},
	}}
	pub := &mockPublisher{}

	p := poller.New(cfg, jira, &mockDedupeStore{isNew: true}, pub, fixedNow(epoch), discardLogger())
	p.PollOnce(context.Background())

	jira.mu.Lock()
	calls := len(jira.calls)
	jira.mu.Unlock()
	if calls != 2 {
		t.Errorf("SearchTransitions called %d times, want 2 (pagination)", calls)
	}

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.calls) != 2 {
		t.Errorf("want 2 Publish calls (one per page), got %d", len(pub.calls))
	}
}

// BT-5: Multiple projects reflected in SearchRequest.
func TestPollOnce_BT5_MultipleProjects(t *testing.T) {
	cfg := testConfig()
	cfg.JiraProjects = []string{"INFRA", "PLAT"}
	cfg.LookbackWindow = time.Hour

	jira := &mockJiraClient{results: []jiraclient.SearchResult{{Issues: []jiraclient.Issue{}}}}

	p := poller.New(cfg, jira, &mockDedupeStore{isNew: true}, &mockPublisher{}, time.Now, discardLogger())
	p.PollOnce(context.Background())

	jira.mu.Lock()
	defer jira.mu.Unlock()
	if len(jira.calls) == 0 {
		t.Fatal("expected SearchTransitions to be called")
	}
	req := jira.calls[0]
	if len(req.Projects) != 2 || req.Projects[0] != "INFRA" || req.Projects[1] != "PLAT" {
		t.Errorf("Projects = %v, want [INFRA PLAT]", req.Projects)
	}
}

