package poller

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ghwatch/event"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ghwatch/formatter"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ghwatch/state"
)

// EventPoller is the interface each event source implements.
type EventPoller interface {
	Poll(ctx context.Context) ([]event.Event, error)
	Name() string
}

// Dispatcher runs multiple pollers on a shared ticker, deduplicates events,
// formats output, and periodically saves state.
type Dispatcher struct {
	pollers  []EventPoller
	interval time.Duration
	dedup    *event.Deduplicator
	store    *state.Store
	fmt      formatter.Formatter
	debug    bool
	logger   *log.Logger
	eventCh  chan event.Event
}

// DispatcherConfig configures a Dispatcher.
type DispatcherConfig struct {
	Pollers   []EventPoller
	Interval  time.Duration
	Dedup     *event.Deduplicator
	Store     *state.Store
	Formatter formatter.Formatter
	Debug     bool
	Logger    *log.Logger
}

// NewDispatcher creates a polling dispatcher.
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	return &Dispatcher{
		pollers:  cfg.Pollers,
		interval: cfg.Interval,
		dedup:    cfg.Dedup,
		store:    cfg.Store,
		fmt:      cfg.Formatter,
		debug:    cfg.Debug,
		logger:   cfg.Logger,
		eventCh:  make(chan event.Event, 256),
	}
}

func (d *Dispatcher) logf(format string, args ...interface{}) {
	if d.debug && d.logger != nil {
		d.logger.Printf(format, args...)
	}
}

// Run starts all pollers and blocks until ctx is cancelled.
// On shutdown it waits for pollers to finish, drains the event channel,
// saves final state, and returns ctx.Err().
func (d *Dispatcher) Run(ctx context.Context) error {
	var pollerWg sync.WaitGroup

	// Launch one goroutine per poller.
	for _, p := range d.pollers {
		pollerWg.Add(1)
		go func(p EventPoller) {
			defer pollerWg.Done()
			d.runPoller(ctx, p)
		}(p)
	}

	// Formatter goroutine: reads from eventCh, writes formatted output.
	fmtDone := make(chan struct{})
	go func() {
		defer close(fmtDone)
		for ev := range d.eventCh {
			if err := d.fmt.Format(ev); err != nil {
				d.logf("format error: %v", err)
			}
		}
	}()

	// Periodic state save + dedup prune.
	saveTicker := time.NewTicker(5 * time.Minute)
	go func() {
		for {
			select {
			case <-saveTicker.C:
				d.dedup.Prune()
				d.saveState()
			case <-ctx.Done():
				return
			}
		}
	}()

	// Block until ctx is cancelled.
	<-ctx.Done()
	saveTicker.Stop()
	d.logf("shutting down pollers")

	// Wait for pollers to exit, then close channel so formatter drains.
	pollerWg.Wait()
	close(d.eventCh)
	<-fmtDone

	d.saveState()
	return ctx.Err()
}

func (d *Dispatcher) runPoller(ctx context.Context, p EventPoller) {
	// Do an initial poll immediately.
	d.pollOnce(ctx, p)

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.pollOnce(ctx, p)
		case <-ctx.Done():
			return
		}
	}
}

func (d *Dispatcher) pollOnce(ctx context.Context, p EventPoller) {
	d.logf("[%s] polling", p.Name())
	events, err := p.Poll(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return // Context cancelled, not a real error.
		}
		d.logf("[%s] poll error: %v", p.Name(), err)
		return
	}
	d.logf("[%s] got %d events", p.Name(), len(events))

	emitted := 0
	duped := 0
	for _, ev := range events {
		if d.dedup.IsDuplicate(ev.ID) {
			d.logf("[%s] dedup skip id=%s", p.Name(), ev.ID)
			duped++
			continue
		}
		d.dedup.Mark(ev.ID)
		d.logf("[%s] emit id=%s kind=%s", p.Name(), ev.ID, ev.Kind)
		select {
		case d.eventCh <- ev:
			emitted++
		case <-ctx.Done():
			return
		}
	}
	if len(events) > 0 {
		d.logf("[%s] emitted=%d duped=%d", p.Name(), emitted, duped)
	}
}

func (d *Dispatcher) saveState() {
	if d.store == nil {
		return
	}
	d.store.SetSeenEvents(d.dedup.SeenIDs())
	d.store.SetLastPollTime(time.Now())
	if err := d.store.Save(); err != nil {
		d.logf("state save error: %v", err)
	} else {
		d.logf("state saved")
	}
}

// Events returns a read-only channel of events (for external consumers).
func (d *Dispatcher) Events() <-chan event.Event {
	return d.eventCh
}
