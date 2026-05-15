package main

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// AuthProvider declares a named, checkable auth dependency.
// Sources declare deps by name; the registry checks Ready() before starting each source.
type AuthProvider interface {
	Name() string // e.g. "bluesky", "google_youtube"
	Ready() bool  // false = credentials not yet available
}

// blueskyAuthProvider wraps *BlueskyClient as an AuthProvider.
type blueskyAuthProvider struct{ client *BlueskyClient }

func (p *blueskyAuthProvider) Name() string { return "bluesky" }
func (p *blueskyAuthProvider) Ready() bool  { return p.client != nil }

// googleYouTubeAuthProvider wraps Google OAuth client credentials as an AuthProvider.
type googleYouTubeAuthProvider struct{ clientID string }

func (p *googleYouTubeAuthProvider) Name() string { return "google_youtube" }
func (p *googleYouTubeAuthProvider) Ready() bool  { return p.clientID != "" }

// ContentSource is implemented by every ingestion source.
// Name() returns a stable, lowercase snake_case identifier used as the
// seen_content.source dedup key. WARNING: Name() must never change after
// first deployment — changing it silently discards all prior dedup history
// for that source and re-enqueues previously seen content.
// AuthDeps() returns the names of auth providers this source requires.
// The registry checks all deps are Ready() before calling Start().
// Start() may assume auth is available — no nil-guard needed inside Start().
type ContentSource interface {
	Name() string
	AuthDeps() []string
	Start(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error
}

// SourceHealth is the JSON-serializable health snapshot for one source.
type SourceHealth struct {
	Name           string     `json:"name"`
	Status         string     `json:"status"` // "running", "stopped", "error", "skipped"
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	LastCompleteAt *time.Time `json:"last_complete_at,omitempty"`
	ErrorCount     int64      `json:"error_count"`
	ItemsEnqueued  int64      `json:"items_enqueued"`
}

// sourceHealthRecord tracks live health state for one source.
type sourceHealthRecord struct {
	mu             sync.RWMutex
	status         string
	lastRunAt      time.Time
	lastCompleteAt time.Time
	errorCount     atomic.Int64
	itemsEnqueued  atomic.Int64
}

func (h *sourceHealthRecord) setStatus(s string) {
	h.mu.Lock()
	h.status = s
	h.mu.Unlock()
}

func (h *sourceHealthRecord) setLastRunAt(t time.Time) {
	h.mu.Lock()
	h.lastRunAt = t
	h.mu.Unlock()
}

func (h *sourceHealthRecord) setLastCompleteAt(t time.Time) {
	h.mu.Lock()
	h.lastCompleteAt = t
	h.mu.Unlock()
}

func (h *sourceHealthRecord) snapshot(name string) SourceHealth {
	h.mu.RLock()
	defer h.mu.RUnlock()
	sh := SourceHealth{
		Name:          name,
		Status:        h.status,
		ErrorCount:    h.errorCount.Load(),
		ItemsEnqueued: h.itemsEnqueued.Load(),
	}
	if !h.lastRunAt.IsZero() {
		t := h.lastRunAt
		sh.LastRunAt = &t
	}
	if !h.lastCompleteAt.IsZero() {
		t := h.lastCompleteAt
		sh.LastCompleteAt = &t
	}
	return sh
}

// SourceRegistry manages source lifecycle.
type SourceRegistry struct {
	sources       []ContentSource
	authProviders map[string]AuthProvider
	health        map[string]*sourceHealthRecord
	healthMu      sync.RWMutex
}

// NewSourceRegistry returns an empty registry.
func NewSourceRegistry() *SourceRegistry {
	return &SourceRegistry{
		authProviders: make(map[string]AuthProvider),
		health:        make(map[string]*sourceHealthRecord),
	}
}

// Register adds a source. Must be called before Start.
func (r *SourceRegistry) Register(src ContentSource) {
	r.sources = append(r.sources, src)
	r.healthMu.Lock()
	r.health[src.Name()] = &sourceHealthRecord{status: "registered"}
	r.healthMu.Unlock()
	slog.Info("source_registered", "source", src.Name(), "total_sources", len(r.sources))
}

// RegisterAuth adds a named auth provider. Must be called before Start.
func (r *SourceRegistry) RegisterAuth(name string, provider AuthProvider) {
	r.authProviders[name] = provider
}

// HealthSnapshot returns a point-in-time snapshot of all registered sources.
func (r *SourceRegistry) HealthSnapshot() []SourceHealth {
	r.healthMu.RLock()
	names := make([]string, 0, len(r.health))
	for name := range r.health {
		names = append(names, name)
	}
	r.healthMu.RUnlock()

	out := make([]SourceHealth, 0, len(names))
	r.healthMu.RLock()
	defer r.healthMu.RUnlock()
	for _, name := range names {
		if h, ok := r.health[name]; ok {
			out = append(out, h.snapshot(name))
		}
	}
	return out
}

// Start launches all registered sources in goroutines with panic recovery.
// Sources whose auth deps are missing or not ready are skipped before launch.
// If q is nil, all sources are skipped with source_skipped_missing_queue.
// A panic in one source does not affect other sources.
// Blocks until ctx is cancelled.
func (r *SourceRegistry) Start(ctx context.Context, q *Queue, emit func(*ShareRequest) error) {
	if q == nil {
		for _, src := range r.sources {
			slog.Warn("source_skipped_missing_queue", "source", src.Name())
			r.healthRecord(src.Name()).setStatus("skipped")
		}
		<-ctx.Done()
		return
	}

	var wg sync.WaitGroup
	for _, src := range r.sources {
		src := src
		h := r.healthRecord(src.Name())

		// Check auth deps before launching goroutine.
		skip := false
		for _, dep := range src.AuthDeps() {
			provider, ok := r.authProviders[dep]
			if !ok {
				slog.Warn("source_skipped_unregistered_auth", "source", src.Name(), "dep", dep)
				skip = true
				break
			}
			if !provider.Ready() {
				slog.Warn("source_skipped_missing_auth", "source", src.Name(), "dep", dep)
				skip = true
				break
			}
		}
		if skip {
			h.setStatus("skipped")
			continue
		}

		// Wrap emit to count items enqueued per source.
		countingEmit := func(req *ShareRequest) error {
			err := emit(req)
			if err == nil {
				h.itemsEnqueued.Add(1)
			}
			return err
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					h.errorCount.Add(1)
					h.setStatus("error")
					slog.Error("source_start_panic",
						"source", src.Name(),
						"panic", rec,
						"stack", string(debug.Stack()),
					)
				}
			}()
			h.setStatus("running")
			h.setLastRunAt(time.Now())
			slog.Info("source_start", "source", src.Name())
			if err := src.Start(ctx, q, countingEmit); err != nil {
				h.errorCount.Add(1)
				h.setStatus("error")
				slog.Warn("source_start_error", "source", src.Name(), "error", err)
			} else {
				h.setStatus("stopped")
				h.setLastCompleteAt(time.Now())
				slog.Info("source_complete", "source", src.Name())
			}
		}()
	}
	<-ctx.Done()
	wg.Wait()
}

// healthRecord returns the health record for a source, creating if missing.
func (r *SourceRegistry) healthRecord(name string) *sourceHealthRecord {
	r.healthMu.RLock()
	h, ok := r.health[name]
	r.healthMu.RUnlock()
	if ok {
		return h
	}
	r.healthMu.Lock()
	defer r.healthMu.Unlock()
	if h, ok = r.health[name]; ok {
		return h
	}
	h = &sourceHealthRecord{status: "registered"}
	r.health[name] = h
	return h
}

// registeredSources returns production content sources filtered by config flags.
// Sources with enabled=false are skipped and emit a source_disabled event.
// EPIC-097 F1: Per-source enable/disable via [server.sources] config block.
// Auth providers are registered separately via registry.RegisterAuth().
func registeredSources(srv *Server) []ContentSource {
	sc := srv.serverConfig.Sources
	var sources []ContentSource

	if sc.BlueskyFirehoseEnabled {
		sources = append(sources, &BlueskyFirehoseSource{client: srv.bskyClient})
	} else {
		emitSourceDisabled(srv, "bsky_firehose")
	}

	if sc.YouTubeWatchLaterEnabled {
		sources = append(sources, &YouTubeWatchLaterSource{
			clientID:     srv.googleClientID,
			clientSecret: srv.googleClientSecret,
			events:       srv.events,
			autoEnqueue:  srv.serverConfig.YouTube.AutoEnqueueWatchLater, // EPIC-098 F3
		})
	} else {
		emitSourceDisabled(srv, "yt_watch_later")
	}

	if sc.YouTubeLikedEnabled {
		sources = append(sources, &YouTubeLikedSource{
			clientID:     srv.googleClientID,
			clientSecret: srv.googleClientSecret,
			events:       srv.events,
			autoEnqueue:  true, // EPIC-098 F3: yt_liked uses simple auto-enqueue for now
		})
	} else {
		emitSourceDisabled(srv, "yt_liked")
	}

	if sc.YouTubeMonitoredEnabled {
		sources = append(sources, &YouTubeSubsSource{
			clientID:     srv.googleClientID,
			clientSecret: srv.googleClientSecret,
			events:       srv.events,
			autoEnqueue:  srv.serverConfig.YouTube.AutoEnqueueSubscriptions, // EPIC-098 F3
		})
	} else {
		emitSourceDisabled(srv, "yt_monitored")
	}

	if len(sources) == 0 {
		slog.Warn("sources_all_disabled", "count", 0)
	}

	return sources
}

// emitSourceDisabled emits a source_disabled event for a source skipped by config.
func emitSourceDisabled(srv *Server, sourceName string) {
	if srv.events != nil {
		_ = srv.events.Emit("source_disabled", map[string]interface{}{
			"source": sourceName,
			"reason": "config_disabled",
		})
	}
	slog.Info("source_disabled", "source", sourceName, "reason", "config_disabled")
}
