// Package wsgateway serves the realtime WebSocket endpoint GET /api/v1/ws:
// authentication (the request's principal) plus the Origin check, the hello
// frame, resume and replay from the event log, live delivery from the hub,
// heartbeats, client signal frames and the close codes of docs/protocol.md.
package wsgateway

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/httpapi"
	"github.com/donaldsteele/bubbles/internal/store"
)

// Heartbeat defaults (plan pipeline step 3).
const (
	DefaultPingInterval = 25 * time.Second
	DefaultDeadAfter    = 60 * time.Second
	// closeAllWait bounds how long CloseAll waits for the sockets to finish
	// their close handshake.
	closeAllWait = 6 * time.Second
)

// Options configures New.
type Options struct {
	// Hub delivers live events. Required.
	Hub *events.Hub
	// DB holds the instance row and the event log for resume. Required.
	DB *store.DB
	// Version is reported as server_version in hello.
	Version string
	// Logger receives connection failures; nil discards them.
	Logger *slog.Logger
	// PingInterval is the server ping period; 0 means DefaultPingInterval.
	PingInterval time.Duration
	// DeadAfter closes a connection that answered no ping for this long
	// (and bounds a single write); 0 means DefaultDeadAfter.
	DeadAfter time.Duration
}

// Gateway is the WebSocket handler. Mount it behind the auth middleware so
// the request carries its principal.
type Gateway struct {
	o Options

	mu    sync.Mutex
	conns map[*conn]struct{}
}

// New returns a gateway.
func New(o Options) *Gateway {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.PingInterval <= 0 {
		o.PingInterval = DefaultPingInterval
	}
	if o.DeadAfter <= o.PingInterval {
		o.DeadAfter = max(DefaultDeadAfter, 2*o.PingInterval)
	}
	return &Gateway{o: o, conns: map[*conn]struct{}{}}
}

// ServeHTTP authenticates and upgrades the request, then serves the
// connection until it closes. Browser (cookie) principals must send an
// Origin that matches the request host, which blocks cross-site WebSocket
// hijacking; bearer clients are exempt.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := auth.ErrorFrom(r.Context()); err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, httpapi.CodeUnavailable, "authentication is temporarily unavailable")
		return
	}
	p := auth.PrincipalFrom(r.Context())
	if p == nil || p.User == nil {
		httpapi.WriteError(w, http.StatusUnauthorized, httpapi.CodeUnauthorized, "authentication required")
		return
	}
	if p.Via == auth.ViaCookie && !httpapi.SameOrigin(r) {
		httpapi.WriteError(w, http.StatusForbidden, httpapi.CodeForbidden, "cross-origin WebSocket refused")
		return
	}
	q, err := parseQuery(r.URL.Query())
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, httpapi.CodeBadRequest, err.Error())
		return
	}
	// The library's own Origin check (host must match when an Origin is
	// sent) stays on as a second layer.
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept wrote the HTTP error
	}
	c := newConn(g, ws, p, q)
	g.track(c, true)
	defer func() {
		g.track(c, false)
		close(c.done)
	}()
	c.serve(r.Context())
}

func (g *Gateway) track(c *conn, add bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if add {
		g.conns[c] = struct{}{}
	} else {
		delete(g.conns, c)
	}
}

// CloseAll closes every open socket with code and reason (4010
// server_reset, 4011 content_cleared, 1001 on shutdown) and waits, bounded,
// until their handlers have finished.
func (g *Gateway) CloseAll(code int, reason string) {
	g.mu.Lock()
	all := make([]*conn, 0, len(g.conns))
	for c := range g.conns {
		all = append(all, c)
	}
	g.mu.Unlock()
	for _, c := range all {
		c.requestClose(code, reason)
	}
	deadline := time.NewTimer(closeAllWait)
	defer deadline.Stop()
	for _, c := range all {
		select {
		case <-c.done:
		case <-deadline.C:
			return
		}
	}
}

// Conns returns the number of open sockets.
func (g *Gateway) Conns() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.conns)
}
