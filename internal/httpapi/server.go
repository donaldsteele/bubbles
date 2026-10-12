// Package httpapi is the HTTPS front end: router, middleware (request id,
// panic recovery, LAN guard, security headers), the JSON error model, the
// REST handlers and the embedded single-page app.
package httpapi

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/donaldsteele/bubbles/internal/alerts"
	"github.com/donaldsteele/bubbles/internal/apps"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/files"
	"github.com/donaldsteele/bubbles/internal/hooks"
	"github.com/donaldsteele/bubbles/internal/netutil"
	"github.com/donaldsteele/bubbles/internal/push"
	"github.com/donaldsteele/bubbles/internal/search"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/internal/webui"
)

// Deps are the collaborators of the HTTPS handler.
type Deps struct {
	// Version is reported by /healthz.
	Version string
	// Logger receives panics and server errors; nil discards them.
	Logger *slog.Logger
	// AllowedCIDRs returns the extra source ranges admitted besides private
	// and loopback addresses (admin setting allowed_cidrs); nil means none.
	AllowedCIDRs func() []netip.Prefix
	// SPA serves the single-page app files; nil means webui.Handler().
	SPA http.Handler
	// Auth serves the status, login, invite, register, password-reset and
	// /me endpoints; nil leaves those routes out.
	Auth *auth.Service
	// Chat serves the users, channels, DMs, messages, reactions, pins and
	// bootstrap endpoints; nil leaves them out. It needs Auth.
	Chat *chat.Service
	// Files serves POST /files and GET /files/{id}/{name}; nil leaves them
	// out. It needs Auth.
	Files *files.Service
	// Search serves GET /search; nil leaves it out. It needs Auth.
	Search *search.Service
	// Push serves the /push endpoints (VAPID key, subscriptions); nil leaves
	// them out. It needs Auth.
	Push *push.Service
	// Apps serves the app endpoints (GET /apps/self, pairing, /admin/apps,
	// /admin/pairing-requests) and authenticates app bearer tokens on the
	// routes an app may call; nil leaves them out and bearer tokens are not
	// recognised. It needs Auth.
	Apps *apps.Service
	// Alerts serves the /alerts endpoints; nil leaves them out. It needs
	// Auth and Apps.
	Alerts *alerts.Service
	// Hooks serves POST /hooks/{token} and the /admin/webhooks endpoints; nil
	// leaves them out. It needs Auth.
	Hooks *hooks.Service
	// Endpoint says how clients reach this server (host, HTTPS port, CA
	// fingerprint); it fills the connection string of new app tokens and the
	// CA fingerprint of pairing requests. nil means the request host and no
	// fingerprint.
	Endpoint func() apps.Endpoint
	// Now is the clock of the message-post rate limit; nil means time.Now.
	Now func() time.Time
	// WS serves the realtime WebSocket endpoint GET /api/v1/ws
	// (internal/wsgateway). It is mounted behind the cookie authentication
	// middleware and checks the Origin itself; nil (or nil Auth) leaves the
	// route out.
	WS http.Handler
	// Reset serves the Danger-zone endpoints (reset preview, reset, backup
	// download); nil leaves them out.
	Reset Resetter
	// Admin supplies the process state and actions of the admin overview and
	// TLS endpoints; nil leaves /admin/tls out and the overview without them.
	Admin AdminHost
	// DB serves the audit log and the client-isolation hint of the admin API;
	// nil leaves /admin/audit and /admin/tls out.
	DB *store.DB
}

// New returns the HTTPS handler: request id -> recover -> LAN guard ->
// security headers -> router. API routes add authentication, the Origin
// check and the rate limits per route (see registerAuthRoutes).
func New(d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	if d.AllowedCIDRs == nil {
		d.AllowedCIDRs = func() []netip.Prefix { return nil }
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.SPA == nil {
		d.SPA = webui.Handler()
	}
	mux := http.NewServeMux()
	routes(mux, d)
	return chain(mux,
		requestID,
		recoverer(d.Logger),
		netutil.GuardFunc(d.AllowedCIDRs),
		securityHeaders,
	)
}

// NewServer returns the HTTPS server for h. Certificates come from getCert
// on every handshake, so a re-issued leaf is served without a restart.
// Serve it with srv.ServeTLS(ln, "", "").
func NewServer(h http.Handler, getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error), logger *slog.Logger) *http.Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &http.Server{
		Handler: h,
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: getCert,
		},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		// Handshake failures from devices that have not installed the CA yet
		// are routine; keep them out of the default log level.
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelDebug),
	}
}
