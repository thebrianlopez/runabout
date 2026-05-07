package main

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
)

// ContentSource is implemented by every ingestion source.
// Name() returns a stable, lowercase snake_case identifier used as the
// seen_content.source dedup key. WARNING: Name() must never change after
// first deployment — changing it silently discards all prior dedup history
// for that source and re-enqueues previously seen content.
type ContentSource interface {
	Name() string
	Start(ctx context.Context, q *Queue, emit func(*ShareRequest) error) error
}

// SourceRegistry manages source lifecycle.
type SourceRegistry struct {
	sources []ContentSource
}

// NewSourceRegistry returns an empty registry.
func NewSourceRegistry() *SourceRegistry {
	return &SourceRegistry{}
}

// Register adds a source. Must be called before Start.
func (r *SourceRegistry) Register(src ContentSource) {
	r.sources = append(r.sources, src)
	slog.Info("source_registered", "source", src.Name(), "total_sources", len(r.sources))
}

// Start launches all registered sources in goroutines with panic recovery.
// If q is nil, all sources are skipped with source_skipped_missing_queue.
// A panic in one source does not affect other sources.
// Blocks until ctx is cancelled.
func (r *SourceRegistry) Start(ctx context.Context, q *Queue, emit func(*ShareRequest) error) {
	if q == nil {
		for _, src := range r.sources {
			slog.Warn("source_skipped_missing_queue", "source", src.Name())
		}
		<-ctx.Done()
		return
	}

	var wg sync.WaitGroup
	for _, src := range r.sources {
		src := src
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					slog.Error("source_start_panic",
						"source", src.Name(),
						"panic", rec,
						"stack", string(debug.Stack()),
					)
				}
			}()
			slog.Info("source_started", "source", src.Name())
			if err := src.Start(ctx, q, emit); err != nil {
				slog.Warn("source_start_error", "source", src.Name(), "error", err)
			} else {
				slog.Info("source_stopped", "source", src.Name())
			}
		}()
	}
	<-ctx.Done()
	wg.Wait()
}

// registeredSources returns all production content sources.
// YouTube sources poll every hour; their Start() is a no-op-loop when credentials are empty.
// BlueskyFirehoseSource Start() is a no-op when bskyClient is nil.
func registeredSources(srv *Server) []ContentSource {
	return []ContentSource{
		&BlueskyFirehoseSource{client: srv.bskyClient},
		&YouTubeWatchLaterSource{clientID: srv.googleClientID, clientSecret: srv.googleClientSecret, events: srv.events},
		&YouTubeLikedSource{clientID: srv.googleClientID, clientSecret: srv.googleClientSecret, events: srv.events},
		&YouTubeSubsSource{clientID: srv.googleClientID, clientSecret: srv.googleClientSecret, events: srv.events},
	}
}
