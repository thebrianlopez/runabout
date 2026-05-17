package poller

import (
	"context"
	"errors"
	"log"
	"os"
	"testing"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ghwatch/event"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ghwatch/formatter"
)

// fakePoller implements EventPoller for testing.
type fakePoller struct {
	name   string
	events []event.Event
	err    error
	calls  int
}

func (f *fakePoller) Poll(_ context.Context) ([]event.Event, error) {
	f.calls++
	return f.events, f.err
}

func (f *fakePoller) Name() string { return f.name }

// fakeFormatter implements formatter.Formatter for testing.
type fakeFormatter struct {
	events []event.Event
	err    error
}

func (f *fakeFormatter) Format(ev event.Event) error {
	f.events = append(f.events, ev)
	return f.err
}

// Ensure interfaces are satisfied at compile time.
var _ EventPoller = (*fakePoller)(nil)
var _ formatter.Formatter = (*fakeFormatter)(nil)

func TestNewDispatcher(t *testing.T) {
	dedup := event.NewDeduplicator(time.Hour)
	fmtr := &fakeFormatter{}
	d := NewDispatcher(DispatcherConfig{
		Pollers:   []EventPoller{&fakePoller{name: "test"}},
		Interval:  time.Second,
		Dedup:     dedup,
		Formatter: fmtr,
	})
	if d == nil {
		t.Fatal("NewDispatcher returned nil")
	}
	if len(d.pollers) != 1 {
		t.Errorf("pollers = %d, want 1", len(d.pollers))
	}
	if d.interval != time.Second {
		t.Errorf("interval = %v, want 1s", d.interval)
	}
	if cap(d.eventCh) != 256 {
		t.Errorf("eventCh cap = %d, want 256", cap(d.eventCh))
	}
}

func TestRunCancellation(t *testing.T) {
	dedup := event.NewDeduplicator(time.Hour)
	fmtr := &fakeFormatter{}
	fp := &fakePoller{
		name: "cancel-test",
		events: []event.Event{
			{ID: "ev-1", Kind: event.KindPush, Repo: "o/r", Timestamp: time.Now()},
		},
	}

	d := NewDispatcher(DispatcherConfig{
		Pollers:   []EventPoller{fp},
		Interval:  100 * time.Millisecond,
		Dedup:     dedup,
		Formatter: fmtr,
	})

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after pollers have time to run at least once.
	go func() {
		time.Sleep(250 * time.Millisecond)
		cancel()
	}()

	err := d.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run error = %v, want context.Canceled", err)
	}
	if fp.calls < 1 {
		t.Errorf("poller called %d times, want >= 1", fp.calls)
	}
	if len(fmtr.events) < 1 {
		t.Errorf("formatted %d events, want >= 1", len(fmtr.events))
	}
}

func TestPollOnceDedup(t *testing.T) {
	dedup := event.NewDeduplicator(time.Hour)
	fmtr := &fakeFormatter{}
	now := time.Now()

	fp := &fakePoller{
		name: "dedup-test",
		events: []event.Event{
			{ID: "dup-1", Kind: event.KindPR, Repo: "o/r", Timestamp: now},
			{ID: "dup-2", Kind: event.KindPR, Repo: "o/r", Timestamp: now},
			{ID: "dup-1", Kind: event.KindPR, Repo: "o/r", Timestamp: now}, // duplicate
		},
	}

	d := NewDispatcher(DispatcherConfig{
		Pollers:   []EventPoller{fp},
		Interval:  time.Hour,
		Dedup:     dedup,
		Formatter: fmtr,
	})

	// Start a consumer goroutine to drain eventCh.
	done := make(chan struct{})
	var received []event.Event
	go func() {
		defer close(done)
		for ev := range d.eventCh {
			received = append(received, ev)
		}
	}()

	ctx := context.Background()
	d.pollOnce(ctx, fp)
	close(d.eventCh)
	<-done

	if len(received) != 2 {
		t.Errorf("received %d events, want 2 (dedup should remove 1)", len(received))
	}
}

func TestPollOnceError(t *testing.T) {
	dedup := event.NewDeduplicator(time.Hour)
	fmtr := &fakeFormatter{}
	logger := log.New(os.Stderr, "[test] ", 0)

	fp := &fakePoller{
		name: "err-test",
		err:  errors.New("api timeout"),
	}

	d := NewDispatcher(DispatcherConfig{
		Pollers:   []EventPoller{fp},
		Interval:  time.Hour,
		Dedup:     dedup,
		Formatter: fmtr,
		Debug:     true,
		Logger:    logger,
	})

	ctx := context.Background()
	d.pollOnce(ctx, fp)

	// No events should be emitted.
	select {
	case ev := <-d.eventCh:
		t.Errorf("unexpected event: %+v", ev)
	default:
		// Expected: no event.
	}
}

func TestEventsAccessor(t *testing.T) {
	dedup := event.NewDeduplicator(time.Hour)
	fmtr := &fakeFormatter{}

	d := NewDispatcher(DispatcherConfig{
		Pollers:   []EventPoller{&fakePoller{name: "test"}},
		Interval:  time.Second,
		Dedup:     dedup,
		Formatter: fmtr,
	})

	ch := d.Events()
	if ch == nil {
		t.Fatal("Events() returned nil")
	}

	// Send an event through eventCh, verify it's readable from Events().
	go func() {
		d.eventCh <- event.Event{ID: "accessor-1", Kind: event.KindPush}
	}()

	select {
	case ev := <-ch:
		if ev.ID != "accessor-1" {
			t.Errorf("event ID = %q, want accessor-1", ev.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out reading from Events()")
	}
}

func TestSaveStateNilStore(t *testing.T) {
	dedup := event.NewDeduplicator(time.Hour)
	fmtr := &fakeFormatter{}

	d := NewDispatcher(DispatcherConfig{
		Pollers:   []EventPoller{&fakePoller{name: "test"}},
		Interval:  time.Second,
		Dedup:     dedup,
		Store:     nil, // no store
		Formatter: fmtr,
	})

	// Should not panic with nil store.
	d.saveState()
}

func TestLogfDebugOnOff(t *testing.T) {
	dedup := event.NewDeduplicator(time.Hour)
	fmtr := &fakeFormatter{}

	// Debug off: logf should be a no-op (no panic).
	d := NewDispatcher(DispatcherConfig{
		Pollers:   []EventPoller{},
		Interval:  time.Second,
		Dedup:     dedup,
		Formatter: fmtr,
		Debug:     false,
		Logger:    nil,
	})
	d.logf("should not panic %d", 42)

	// Debug on with logger: should not panic.
	logger := log.New(os.Stderr, "[test] ", 0)
	d2 := NewDispatcher(DispatcherConfig{
		Pollers:   []EventPoller{},
		Interval:  time.Second,
		Dedup:     dedup,
		Formatter: fmtr,
		Debug:     true,
		Logger:    logger,
	})
	d2.logf("debug message %d", 42)
}

func TestMultiplePollers(t *testing.T) {
	dedup := event.NewDeduplicator(time.Hour)
	fmtr := &fakeFormatter{}
	now := time.Now()

	p1 := &fakePoller{
		name:   "poller-a",
		events: []event.Event{{ID: "a-1", Kind: event.KindPush, Repo: "o/r", Timestamp: now}},
	}
	p2 := &fakePoller{
		name:   "poller-b",
		events: []event.Event{{ID: "b-1", Kind: event.KindPR, Repo: "o/r", Timestamp: now}},
	}

	d := NewDispatcher(DispatcherConfig{
		Pollers:   []EventPoller{p1, p2},
		Interval:  100 * time.Millisecond,
		Dedup:     dedup,
		Formatter: fmtr,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(250 * time.Millisecond)
		cancel()
	}()

	_ = d.Run(ctx)

	if p1.calls < 1 {
		t.Errorf("poller-a called %d times, want >= 1", p1.calls)
	}
	if p2.calls < 1 {
		t.Errorf("poller-b called %d times, want >= 1", p2.calls)
	}
	if len(fmtr.events) < 2 {
		t.Errorf("formatted %d events, want >= 2", len(fmtr.events))
	}
}
