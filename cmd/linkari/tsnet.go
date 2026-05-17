package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"tailscale.com/ipn"
	"tailscale.com/tsnet"
)

type contextKey string

const realIPKey contextKey = "realIP"

// funnelConnContext extracts the real client IP from Tailscale FunnelConn.Src
// and stores it in the request context. Used as http.Server.ConnContext for
// the Funnel listener so downstream handlers see the actual remote IP rather
// than the Tailscale relay address.
func funnelConnContext(ctx context.Context, c net.Conn) context.Context {
	if fc, ok := c.(*ipn.FunnelConn); ok {
		return context.WithValue(ctx, realIPKey, fc.Src.Addr().String())
	}
	return ctx
}

// realIP returns the real client IP from context (set by funnelConnContext),
// falling back to r.RemoteAddr with port stripped.
func realIPFromContext(ctx context.Context, remoteAddr string) string {
	if ip, ok := ctx.Value(realIPKey).(string); ok && ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// TsnetConfig holds the configuration for the embedded Tailscale node.
type TsnetConfig struct {
	Hostname string
	StateDir string
	AuthKey  string
	Debug    bool
}

// TsnetServer wraps tsnet.Server lifecycle for linkari.
type TsnetServer struct {
	cfg      TsnetConfig
	ts       *tsnet.Server
	listener net.Listener
	fqdn     string
}

// NewTsnetServer creates a TsnetServer. Call Start() to bring up the node.
func NewTsnetServer(cfg TsnetConfig) *TsnetServer {
	return &TsnetServer{cfg: cfg}
}

// Start initializes the tsnet node, authenticates with Tailscale,
// and returns a Funnel net.Listener on :443.
func (t *TsnetServer) Start(ctx context.Context) (net.Listener, error) {
	t.ts = &tsnet.Server{
		Hostname: t.cfg.Hostname,
		Dir:      t.cfg.StateDir,
		AuthKey:  t.cfg.AuthKey,
		// UserLogf surfaces auth URLs and status messages to the user.
		UserLogf: func(format string, args ...any) {
			slog.Info("tsnet", "event_type", "tsnet_event", "msg", fmt.Sprintf(format, args...))
		},
	}
	// Logf controls verbose backend debug logs (wgengine, magicsock, etc).
	// Gated at slog debug level so operators can toggle via --log-level.
	if t.cfg.Debug {
		t.ts.Logf = func(format string, args ...any) {
			slog.Debug("tsnet", "event_type", "tsnet_event", "msg", fmt.Sprintf(format, args...))
		}
	}

	// Up() with a timeout ensures we surface auth errors quickly rather
	// than blocking forever. ListenFunnel calls Up() internally with
	// context.Background(), so this guards against hanging on NeedsLogin.
	startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	status, err := t.ts.Up(startCtx)
	if err != nil {
		t.ts.Close()
		t.ts = nil
		return nil, fmt.Errorf("tsnet: node failed to start: %w", err)
	}

	// Capture FQDN from status before ListenFunnel.
	if status.Self != nil && status.Self.DNSName != "" {
		t.fqdn = strings.TrimSuffix(status.Self.DNSName, ".")
	} else {
		t.fqdn = t.cfg.Hostname
	}

	// ListenFunnel requires:
	// 1. HTTPS Certificates enabled in Tailscale admin (DNS settings)
	// 2. "funnel" nodeAttr in ACL policy
	// 3. Port 443 (Funnel only supports 443, 8443, 10000)
	ln, err := t.ts.ListenFunnel("tcp", ":443")
	if err != nil {
		t.ts.Close()
		t.ts = nil
		return nil, fmt.Errorf("tsnet: funnel listener: %w", err)
	}

	t.listener = ln
	slog.Info("tsnet Funnel listening",
		"event_type", "tsnet_funnel_up",
		"fqdn", t.fqdn,
		"url", "https://"+t.fqdn,
	)
	return ln, nil
}

// Close shuts down the Funnel listener and the tsnet node.
func (t *TsnetServer) Close() error {
	var firstErr error
	if t.listener != nil {
		if err := t.listener.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if t.ts != nil {
		if err := t.ts.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// FQDN returns the node's full DNS name (e.g. "linkari.<tailnet>.ts.net").
func (t *TsnetServer) FQDN() string {
	return t.fqdn
}
