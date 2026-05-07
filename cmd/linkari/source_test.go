package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubSource is a ContentSource that records whether Start was called.
type stubSource struct {
	name    string
	startFn func(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error
}

func (s *stubSource) Name() string { return s.name }
func (s *stubSource) Start(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
	if s.startFn != nil {
		return s.startFn(ctx, q, emit)
	}
	return nil
}

// CT-1: Registered source's Start() is called when registry starts.
func TestSourceRegistry_CT1_StartCalled(t *testing.T) {
	q := newTestQueue(t)
	var called atomic.Bool
	src := &stubSource{
		name: "ct1_source",
		startFn: func(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
			called.Store(true)
			return nil
		},
	}
	r := NewSourceRegistry()
	r.Register(src)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r.Start(ctx, q, func(req *ShareRequest) error { return nil })

	if !called.Load() {
		t.Error("CT-1: source Start() was not called")
	}
}

// CT-2: Panicking source does not crash other sources.
func TestSourceRegistry_CT2_PanicIsolation(t *testing.T) {
	q := newTestQueue(t)
	var bStarted atomic.Bool

	panicSrc := &stubSource{
		name: "panic_source",
		startFn: func(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
			panic("ct2 deliberate panic")
		},
	}
	goodSrc := &stubSource{
		name: "good_source",
		startFn: func(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
			bStarted.Store(true)
			<-ctx.Done()
			return nil
		},
	}

	r := NewSourceRegistry()
	r.Register(panicSrc)
	r.Register(goodSrc)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r.Start(ctx, q, func(req *ShareRequest) error { return nil })

	if !bStarted.Load() {
		t.Error("CT-2: good source was not started after panic source panicked")
	}
}

// CT-3: Registry with nil queue skips all sources.
func TestSourceRegistry_CT3_NilQueueSkipsAll(t *testing.T) {
	var called atomic.Bool
	src := &stubSource{
		name: "ct3_source",
		startFn: func(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
			called.Store(true)
			return nil
		},
	}
	r := NewSourceRegistry()
	r.Register(src)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r.Start(ctx, nil, func(req *ShareRequest) error { return nil })

	if called.Load() {
		t.Error("CT-3: source Start() was called despite nil queue")
	}
}

// CT-4: Name() returns stable identifier used in log fields.
func TestSourceRegistry_CT4_NameStable(t *testing.T) {
	src := &stubSource{name: "ct4_test_source"}
	if got := src.Name(); got != "ct4_test_source" {
		t.Errorf("CT-4: Name() = %q, want %q", got, "ct4_test_source")
	}
}

// CT-5: emit is called synchronously — source blocks until emit returns.
func TestSourceRegistry_CT5_EmitSynchronous(t *testing.T) {
	q := newTestQueue(t)
	var order []string
	var mu sync.Mutex

	appendOrder := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, s)
	}

	emit := func(req *ShareRequest) error {
		appendOrder("emit_start")
		time.Sleep(20 * time.Millisecond)
		appendOrder("emit_end")
		return nil
	}

	src := &stubSource{
		name: "ct5_source",
		startFn: func(ctx context.Context, q *Queue, emitFn func(*ShareRequest) error) error {
			appendOrder("before_emit")
			emitFn(&ShareRequest{URL: "https://ct5.test"})
			appendOrder("after_emit")
			return nil
		},
	}

	r := NewSourceRegistry()
	r.Register(src)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	r.Start(ctx, q, emit)

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()

	want := []string{"before_emit", "emit_start", "emit_end", "after_emit"}
	if len(got) != len(want) {
		t.Fatalf("CT-5: emit order %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("CT-5: order[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// CT-6: Two sources registered — both start.
func TestSourceRegistry_CT6_BothStart(t *testing.T) {
	q := newTestQueue(t)
	var aStarted, bStarted atomic.Bool

	srcA := &stubSource{
		name: "source_a",
		startFn: func(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
			aStarted.Store(true)
			return nil
		},
	}
	srcB := &stubSource{
		name: "source_b",
		startFn: func(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
			bStarted.Store(true)
			return nil
		},
	}

	r := NewSourceRegistry()
	r.Register(srcA)
	r.Register(srcB)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r.Start(ctx, q, func(req *ShareRequest) error { return nil })

	if !aStarted.Load() {
		t.Error("CT-6: source A Start() was not called")
	}
	if !bStarted.Load() {
		t.Error("CT-6: source B Start() was not called")
	}
}

// Compile-time check: stubSource implements ContentSource.
var _ ContentSource = (*stubSource)(nil)

// BT-1: Source that returns error from Start() is logged as source_start_error.
// We verify the registry doesn't crash and other sources are unaffected.
func TestSourceRegistry_BT1_ErrorLogged(t *testing.T) {
	q := newTestQueue(t)
	var goodStarted atomic.Bool

	errSrc := &stubSource{
		name: "error_source",
		startFn: func(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
			return errors.New("bt1 deliberate error")
		},
	}
	goodSrc := &stubSource{
		name: "bt1_good_source",
		startFn: func(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
			goodStarted.Store(true)
			return nil
		},
	}

	r := NewSourceRegistry()
	r.Register(errSrc)
	r.Register(goodSrc)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r.Start(ctx, q, func(req *ShareRequest) error { return nil })

	if !goodStarted.Load() {
		t.Error("BT-1: good source was not started after error source returned error")
	}
}

// BT-2: ctx cancellation exits all goroutines cleanly.
func TestSourceRegistry_BT2_CtxCancellationExits(t *testing.T) {
	q := newTestQueue(t)
	var exited atomic.Bool

	src := &stubSource{
		name: "bt2_source",
		startFn: func(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
			<-ctx.Done()
			exited.Store(true)
			return nil
		},
	}

	r := NewSourceRegistry()
	r.Register(src)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r.Start(ctx, q, func(req *ShareRequest) error { return nil })

	if !exited.Load() {
		t.Error("BT-2: source goroutine did not exit after ctx cancellation")
	}
}

// BT-3: Zero sources registered — Start is a no-op (no panic).
func TestSourceRegistry_BT3_EmptyRegistryNoop(t *testing.T) {
	q := newTestQueue(t)
	r := NewSourceRegistry()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Should not panic and should return after ctx cancellation.
	r.Start(ctx, q, func(req *ShareRequest) error { return nil })
}

// RG-1: Panicking firehose stub does not affect other sources.
// (CT-2 variant using a realistic firehose-shaped source stub.)
func TestSourceRegistry_RG1_FirehosePanicIsolated(t *testing.T) {
	q := newTestQueue(t)
	var otherStarted atomic.Bool

	firehoseSrc := &stubSource{
		name: "bsky_firehose",
		startFn: func(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
			panic("simulated firehose websocket panic")
		},
	}
	otherSrc := &stubSource{
		name: "yt_watch_later",
		startFn: func(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error {
			otherStarted.Store(true)
			<-ctx.Done()
			return nil
		},
	}

	r := NewSourceRegistry()
	r.Register(firehoseSrc)
	r.Register(otherSrc)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r.Start(ctx, q, func(req *ShareRequest) error { return nil })

	if !otherStarted.Load() {
		t.Error("RG-1: yt_watch_later source was not started after firehose panicked")
	}
}
