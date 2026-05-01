package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ContentType signals to the scoring preamble how to interpret fetched content.
type ContentType int

const (
	ContentTypePlain    ContentType = iota // generic text or Jina fallback
	ContentTypeMarkdown                    // structured markdown (GitHub README, file)
	ContentTypeADF                         // Atlassian Document Format (Confluence page)
	ContentTypeJSON                        // structured JSON (API response)
)

func (ct ContentType) String() string {
	switch ct {
	case ContentTypeMarkdown:
		return "markdown"
	case ContentTypeADF:
		return "adf"
	case ContentTypeJSON:
		return "json"
	default:
		return "plain"
	}
}

// DomainClient is the interface every domain-specific fetcher must satisfy.
type DomainClient interface {
	Fetch(ctx context.Context, u *url.URL) (content string, ct ContentType, err error)
}

// DomainRouter intercepts content fetches and routes to authenticated domain clients.
type DomainRouter struct {
	clients   map[string]DomainClient
	jinaFetch func(ctx context.Context, rawURL string) (string, error)
	timeout   time.Duration
	// onEvent is called for each emitted event (injected in tests; may be nil).
	onEvent func(eventType string, metadata map[string]interface{})
}

// NewDomainRouter constructs a DomainRouter with the provided clients.
// Panics if jinaFetch is nil (per BT-3 — explicit fail-fast over silent nil deref).
func NewDomainRouter(clients map[string]DomainClient, jinaFetch func(ctx context.Context, rawURL string) (string, error)) *DomainRouter {
	if jinaFetch == nil {
		panic("domain_router: jinaFetch must not be nil")
	}
	c := make(map[string]DomainClient, len(clients))
	for k, v := range clients {
		c[k] = v
	}
	return &DomainRouter{
		clients:   c,
		jinaFetch: jinaFetch,
		timeout:   2 * time.Second,
	}
}

// RegisterClient adds a DomainClient for the given hostname at runtime.
// hostname must be a bare host (no scheme, no trailing slash, no port, no spaces).
func (r *DomainRouter) RegisterClient(hostname string, client DomainClient) {
	if strings.ContainsAny(hostname, "/:@? ") || hostname == "" {
		return
	}
	r.clients[hostname] = client
}

// FetchWithFallback routes rawURL to a registered client or falls back to jinaFetch.
// YouTube URLs always bypass registered clients and go directly to jinaFetch.
// Returns ContentTypePlain when jinaFetch is used as the content source.
// Only returns a non-nil error when jinaFetch itself fails.
func (r *DomainRouter) FetchWithFallback(ctx context.Context, rawURL string) (string, ContentType, error) {
	start := time.Now()

	// Emit fetch_start before any dispatch so callers can correlate with fetch_end via event stream.
	{
		host, _ := MatchHost(rawURL)
		_, clientRegistered := r.clients[host]
		// YouTube bypass skips the client map — client_registered is always false for YouTube URLs.
		if IsYouTube(rawURL) {
			clientRegistered = false
		}
		r.emit("domain_router_fetch_start", map[string]interface{}{
			"url":               rawURL,
			"domain":            host,
			"client_registered": clientRegistered,
		})
	}

	// Fast-path: YouTube bypass.
	if IsYouTube(rawURL) {
		content, err := r.jinaFetch(ctx, rawURL)
		latency := time.Since(start).Milliseconds()
		r.emit("domain_router_fetch_end", map[string]interface{}{
			"url":          redactURL(rawURL),
			"domain":       "youtube",
			"client_used":  "jina_youtube_bypass",
			"fallback_used": false,
			"latency_ms":   latency,
			"content_type": ContentTypePlain.String(),
		})
		return content, ContentTypePlain, err
	}

	host, err := MatchHost(rawURL)
	if err != nil {
		// Malformed URL — fall through to Jina.
		content, jinaErr := r.jinaFetch(ctx, rawURL)
		latency := time.Since(start).Milliseconds()
		r.emit("domain_router_fetch_end", map[string]interface{}{
			"url":          redactURL(rawURL),
			"domain":       "jina",
			"client_used":  "jina",
			"fallback_used": false,
			"latency_ms":   latency,
			"content_type": ContentTypePlain.String(),
		})
		return content, ContentTypePlain, jinaErr
	}

	client, ok := r.clients[host]
	if !ok {
		// No registered client — call Jina directly.
		content, jinaErr := r.jinaFetch(ctx, rawURL)
		latency := time.Since(start).Milliseconds()
		r.emit("domain_router_fetch_end", map[string]interface{}{
			"url":          redactURL(rawURL),
			"domain":       "jina",
			"client_used":  "jina",
			"fallback_used": false,
			"latency_ms":   latency,
			"content_type": ContentTypePlain.String(),
		})
		return content, ContentTypePlain, jinaErr
	}

	// Call registered client with per-client timeout.
	clientCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	parsed, _ := url.Parse(rawURL)
	content, ct, clientErr := client.Fetch(clientCtx, parsed)
	if clientErr == nil {
		latency := time.Since(start).Milliseconds()
		r.emit("domain_router_fetch_end", map[string]interface{}{
			"url":          redactURL(rawURL),
			"domain":       host,
			"client_used":  host,
			"fallback_used": false,
			"latency_ms":   latency,
			"content_type": ct.String(),
		})
		return content, ct, nil
	}

	// Client failed — emit auth error event and fall back to Jina.
	r.emit("domain_router_auth_error", map[string]interface{}{
		"url":         redactURL(rawURL),
		"domain":      host,
		"error_class": classifyClientError(clientErr),
		"fallback":    "jina",
	})

	jinaContent, jinaErr := r.jinaFetch(ctx, rawURL)
	latency := time.Since(start).Milliseconds()
	r.emit("domain_router_fetch_end", map[string]interface{}{
		"url":          redactURL(rawURL),
		"domain":       host,
		"client_used":  "jina",
		"fallback_used": true,
		"latency_ms":   latency,
		"content_type": ContentTypePlain.String(),
	})
	return jinaContent, ContentTypePlain, jinaErr
}

// emit dispatches a structured event via onEvent (test hook) or EventLogger (production).
func (r *DomainRouter) emit(eventType string, metadata map[string]interface{}) {
	if r.onEvent != nil {
		r.onEvent(eventType, metadata)
	}
}

// EmitVia wires a production EventLogger into the router's event pipeline.
// Call this after construction to enable persistent event logging.
func (r *DomainRouter) EmitVia(logger *EventLogger) {
	if logger == nil {
		return
	}
	r.onEvent = func(eventType string, metadata map[string]interface{}) {
		_ = logger.Emit(eventType, metadata)
	}
}

// redactURL returns scheme://host only — strips path, query, and fragment.
func redactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
}

// classifyClientError maps an error to one of the taxonomy classes from TDD §4.
func classifyClientError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "401") || strings.Contains(msg, "403") || strings.Contains(msg, "auth"):
		return "auth_error"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") || strings.Contains(msg, "context"):
		return "timeout"
	case strings.Contains(msg, "parse") || strings.Contains(msg, "unmarshal") || strings.Contains(msg, "invalid"):
		return "parse_error"
	default:
		return "network_error"
	}
}
