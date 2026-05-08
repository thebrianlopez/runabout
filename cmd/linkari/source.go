package main

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
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

// SourceRegistry manages source lifecycle.
type SourceRegistry struct {
	sources       []ContentSource
	authProviders map[string]AuthProvider
}

// NewSourceRegistry returns an empty registry.
func NewSourceRegistry() *SourceRegistry {
	return &SourceRegistry{
		authProviders: make(map[string]AuthProvider),
	}
}

// Register adds a source. Must be called before Start.
func (r *SourceRegistry) Register(src ContentSource) {
	r.sources = append(r.sources, src)
	slog.Info("source_registered", "source", src.Name(), "total_sources", len(r.sources))
}

// RegisterAuth adds a named auth provider. Must be called before Start.
func (r *SourceRegistry) RegisterAuth(name string, provider AuthProvider) {
	r.authProviders[name] = provider
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
		}
		<-ctx.Done()
		return
	}

	var wg sync.WaitGroup
	for _, src := range r.sources {
		src := src
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
			continue
		}
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
			slog.Info("source_start", "source", src.Name())
			if err := src.Start(ctx, q, emit); err != nil {
				slog.Warn("source_start_error", "source", src.Name(), "error", err)
			} else {
				slog.Info("source_complete", "source", src.Name())
			}
		}()
	}
	<-ctx.Done()
	wg.Wait()
}

// registeredSources returns all production content sources.
// Auth providers are registered separately via registry.RegisterAuth().
func registeredSources(srv *Server) []ContentSource {
	return []ContentSource{
		&BlueskyFirehoseSource{client: srv.bskyClient},
		&YouTubeWatchLaterSource{clientID: srv.googleClientID, clientSecret: srv.googleClientSecret, events: srv.events},
		&YouTubeLikedSource{clientID: srv.googleClientID, clientSecret: srv.googleClientSecret, events: srv.events},
		&YouTubeSubsSource{clientID: srv.googleClientID, clientSecret: srv.googleClientSecret, events: srv.events},
	}
}
