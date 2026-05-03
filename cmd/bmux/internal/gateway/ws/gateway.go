package ws

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"nhooyr.io/websocket"
)

//go:embed assets
var assetsFS embed.FS

// Gateway manages the WebSocket server lifecycle.
// It also implements http.Handler so it can be mounted in an httptest.Server or
// a custom http.ServeMux.
type Gateway interface {
	http.Handler

	// Start binds and begins accepting connections.
	// When used with httptest.Server, the server is already listening — Start is a no-op.
	Start(ctx context.Context) error

	// Stop closes all connections and stops the listener.
	Stop(ctx context.Context) error

	// ClientCount returns the number of currently connected clients.
	ClientCount() int

	// Addr returns the bound address, e.g. "127.0.0.1:8765".
	Addr() string
}

// Config holds gateway configuration.
type Config struct {
	// Token is the required Bearer token for WebSocket connections.
	Token string

	// MaxClients is the maximum number of concurrent WebSocket clients (default 5).
	MaxClients int

	// BindAddr is the address to bind when Start is called (e.g. "127.0.0.1:8765").
	// Empty means OS-assigned port.
	BindAddr string

	Bridge     ControlModeBridge
	Mirror     MirrorManager
	Registry   SessionRegistry
	Translator KeyTranslator
}

// New creates a new Gateway. It implements http.Handler so it can be used with
// httptest.Server directly (for tests) or standalone via Start.
func New(cfg Config) (Gateway, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("ws.New: Token is required")
	}
	if cfg.MaxClients <= 0 {
		cfg.MaxClients = 5
	}

	stopCtx, stopCancel := context.WithCancel(context.Background())
	g := &gateway{
		cfg:        cfg,
		registry:   newClientRegistry(),
		registry2:  cfg.Registry,
		bridge:     cfg.Bridge,
		mirror:     cfg.Mirror,
		translator: cfg.Translator,
		stopCtx:    stopCtx,
		stopCancel: stopCancel,
	}

	mux := http.NewServeMux()

	// Serve embedded assets at /.
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return nil, fmt.Errorf("ws.New: embed assets: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	// WebSocket endpoint.
	mux.HandleFunc("/ws", g.serveWS)

	g.mux = mux
	return g, nil
}

// gateway is the concrete Gateway implementation.
type gateway struct {
	cfg        Config
	mux        *http.ServeMux
	registry   *ClientRegistry
	registry2  SessionRegistry
	bridge     ControlModeBridge
	mirror     MirrorManager
	translator KeyTranslator

	// standalone server (used when Start is called directly, not via httptest)
	mu      sync.Mutex
	server  *http.Server
	addr    string
	connWG  sync.WaitGroup

	// stopCtx is cancelled by Stop() to tear down all connection goroutines.
	stopCtx    context.Context
	stopCancel context.CancelFunc
}

// ServeHTTP implements http.Handler — allows gateway to be used with httptest.Server.
func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mux.ServeHTTP(w, r)
}

// Start binds and begins accepting connections on cfg.BindAddr (standalone mode).
func (g *gateway) Start(_ context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.server != nil {
		return nil
	}
	ln, err := net.Listen("tcp", g.cfg.BindAddr)
	if err != nil {
		return fmt.Errorf("gateway: listen %s: %w", g.cfg.BindAddr, err)
	}
	srv := &http.Server{Addr: g.cfg.BindAddr, Handler: g.mux}
	g.server = srv
	g.addr = ln.Addr().String()
	go srv.Serve(ln) //nolint:errcheck
	slog.Info("gateway_started", "addr", g.addr)
	return nil
}

// Stop signals all goroutines to exit and waits for completion.
func (g *gateway) Stop(ctx context.Context) error {
	g.stopCancel() // signal all connection goroutines

	g.mu.Lock()
	srv := g.server
	g.mu.Unlock()

	if srv != nil {
		if err := srv.Shutdown(ctx); err != nil {
			return err
		}
	}

	// Wait for all connection goroutines.
	done := make(chan struct{})
	go func() {
		g.connWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	slog.Info("gateway_stopped")
	return nil
}

// ClientCount returns the number of connected clients.
func (g *gateway) ClientCount() int {
	return g.registry.Count()
}

// Addr returns the bound address.
func (g *gateway) Addr() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.addr
}

// serveWS handles WebSocket upgrade and per-connection lifecycle.
func (g *gateway) serveWS(w http.ResponseWriter, r *http.Request) {
	// Auth check — must happen before any WebSocket data is sent.
	if !authMiddleware(g.cfg.Token, r) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err == nil {
			slog.Info("client_auth_failed", "remote", r.RemoteAddr)
			conn.Close(4001, "auth_failed")
		} else {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		}
		return
	}

	// Client limit check — must happen before sending any data.
	clientID := uuid.New().String()
	if !g.registry.Add(clientID, g.cfg.MaxClients) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err == nil {
			slog.Info("client_limit_exceeded", "remote", r.RemoteAddr)
			conn.Close(4008, "client_limit_exceeded")
		} else {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		}
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		g.registry.Remove(clientID)
		slog.Warn("client_accept_error", "error", err)
		return
	}

	slog.Info("client_connected", "client_id", clientID, "clients", g.registry.Count())

	g.connWG.Add(1)
	go func() {
		defer g.connWG.Done()
		g.handleConnection(g.stopCtx, clientID, conn)
		conn.CloseNow()
	}()
}
