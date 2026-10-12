// Package onboarding serves the plain-HTTP onboarding listener: a page that
// explains how to trust the local CA on each platform, the CA certificate in
// DER, PEM and iOS profile form, QR codes for this server's HTTPS URLs and
// invite landing pages. Every other path is redirected to HTTPS. It sets no
// cookies and exposes no API.
package onboarding

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/donaldsteele/bubbles/internal/netutil"
	"github.com/donaldsteele/bubbles/internal/tlsca"
)

//go:embed templates/onboarding.html templates/onboarding.css
var templateFS embed.FS

// ContentSecurityPolicy of every onboarding response: the page needs only
// its stylesheet and the QR image, and works without JavaScript.
const ContentSecurityPolicy = "default-src 'none'; style-src 'self'; img-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Options configure the onboarding handler.
type Options struct {
	// InstanceName returns the display name; nil or "" means "bubbles".
	InstanceName func() string
	// CA returns the local CA, or nil when the server uses a
	// bring-your-own certificate (then no CA is offered for download).
	CA func() *tlsca.CA
	// HTTPSPort is the port the HTTPS listener is bound to.
	HTTPSPort int
	// URLs returns this server's HTTPS base URLs, best first, for example
	// "https://192.168.1.20:8443/".
	URLs func() []string
	// Hosts returns the extra host names and addresses this server answers
	// to (its certificate SANs); QR codes are only drawn for these and for
	// the hosts in URLs.
	Hosts func() []string
	// AllowedCIDRs returns extra source ranges admitted besides private and
	// loopback addresses; nil means none.
	AllowedCIDRs func() []netip.Prefix
	// Logger receives template errors; nil discards them.
	Logger *slog.Logger
}

// New returns the onboarding handler, wrapped in the LAN guard and the
// onboarding security headers.
func New(opts Options) http.Handler {
	s := newServer(opts)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handlePage)
	mux.HandleFunc("GET /onboarding.css", s.handleCSS)
	mux.HandleFunc("GET /ca.crt", s.handleCADER)
	mux.HandleFunc("GET /ca.pem", s.handleCAPEM)
	mux.HandleFunc("GET /ca.mobileconfig", s.handleMobileConfig)
	mux.HandleFunc("GET /qr.svg", s.handleQR)
	mux.HandleFunc("GET /invite/{code}", s.handleInvite)
	mux.HandleFunc("/", s.handleRedirect)

	allowed := opts.AllowedCIDRs
	if allowed == nil {
		allowed = func() []netip.Prefix { return nil }
	}
	return netutil.GuardFunc(allowed)(securityHeaders(mux))
}

// NewServer returns the plain-HTTP server for the onboarding handler h.
func NewServer(h http.Handler, logger *slog.Logger) *http.Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelDebug),
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// server holds the parsed template and the option callbacks.
type server struct {
	opts   Options
	page   *template.Template
	css    []byte
	logger *slog.Logger
}

func newServer(opts Options) *server {
	s := &server{opts: opts, logger: opts.Logger}
	if s.logger == nil {
		s.logger = slog.New(slog.DiscardHandler)
	}
	s.page = template.Must(template.ParseFS(templateFS, "templates/onboarding.html"))
	css, err := templateFS.ReadFile("templates/onboarding.css")
	if err != nil {
		// Unreachable: the file is embedded above.
		panic("onboarding: " + err.Error())
	}
	s.css = css
	return s
}
