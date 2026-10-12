package onboarding

import (
	"bytes"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/donaldsteele/bubbles/internal/tlsca"
)

// inviteCode matches invite codes (base64url / base32 style tokens).
var inviteCode = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// hostName matches a DNS-style host in a Host header.
var hostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// maxQRURL caps the URL a QR code may encode.
const maxQRURL = 512

// pageData feeds templates/onboarding.html.
type pageData struct {
	Instance    string
	HasCA       bool
	CAName      string
	Fingerprint string
	OS          OS
	Firefox     bool
	URLs        []string
	// Target is the URL behind the QR code and the main button.
	Target string
	// Invite is the invite code on /invite/{code}, else "".
	Invite string
	// RegisterURL is the sign-up page of the server while sign-up is open
	// (never on an invite page), else "".
	RegisterURL string
	// SignupClosed is true on the main page while sign-up is closed.
	SignupClosed bool
}

func (s *server) instance() string {
	if s.opts.InstanceName != nil {
		if n := strings.TrimSpace(s.opts.InstanceName()); n != "" {
			return n
		}
	}
	return "bubbles"
}

func (s *server) ca() *tlsca.CA {
	if s.opts.CA == nil {
		return nil
	}
	return s.opts.CA()
}

func (s *server) urls() []string {
	if s.opts.URLs == nil {
		return nil
	}
	return s.opts.URLs()
}

func (s *server) data(r *http.Request) pageData {
	ua := r.UserAgent()
	d := pageData{
		Instance: s.instance(),
		OS:       DetectOS(ua),
		Firefox:  IsFirefox(ua),
		URLs:     s.urls(),
	}
	if ca := s.ca(); ca != nil {
		d.HasCA = true
		d.CAName = ca.Cert.Subject.CommonName
		d.Fingerprint = ca.Fingerprint()
	}
	if len(d.URLs) > 0 {
		d.Target = d.URLs[0]
	}
	return d
}

func (s *server) render(w http.ResponseWriter, d pageData) {
	var buf bytes.Buffer
	if err := s.page.Execute(&buf, d); err != nil {
		s.logger.Error("render onboarding page", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

func (s *server) handlePage(w http.ResponseWriter, r *http.Request) {
	d := s.data(r)
	if s.opts.RegistrationOpen != nil {
		if s.opts.RegistrationOpen() {
			if d.Target != "" {
				d.RegisterURL = strings.TrimSuffix(d.Target, "/") + "/register"
			}
		} else {
			d.SignupClosed = true
		}
	}
	s.render(w, d)
}

func (s *server) handleInvite(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if !inviteCode.MatchString(code) {
		http.Error(w, "invalid invite link", http.StatusNotFound)
		return
	}
	d := s.data(r)
	d.Invite = code
	if d.Target != "" {
		d.Target = strings.TrimSuffix(d.Target, "/") + "/invite/" + code
	}
	s.render(w, d)
}

func (s *server) handleCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(s.css)
}

// serveCA writes a CA download, or 404 when the server has no local CA.
func (s *server) serveCA(w http.ResponseWriter, contentType, filename string, body func(*tlsca.CA) []byte) {
	ca := s.ca()
	if ca == nil {
		http.Error(w, "this server does not use a local certificate authority", http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	h.Set("Cache-Control", "no-cache")
	_, _ = w.Write(body(ca))
}

func (s *server) handleCADER(w http.ResponseWriter, _ *http.Request) {
	s.serveCA(w, "application/x-x509-ca-cert", "bubbles-ca.crt", (*tlsca.CA).DER)
}

func (s *server) handleCAPEM(w http.ResponseWriter, _ *http.Request) {
	s.serveCA(w, "application/x-pem-file", "bubbles-ca.pem", (*tlsca.CA).PEM)
}

func (s *server) handleMobileConfig(w http.ResponseWriter, _ *http.Request) {
	s.serveCA(w, tlsca.MobileConfigContentType, "bubbles-ca.mobileconfig", (*tlsca.CA).MobileConfig)
}

func (s *server) handleQR(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("u")
	if !s.ownURL(u) {
		http.Error(w, "u must be an HTTPS URL of this server", http.StatusBadRequest)
		return
	}
	svg, err := qrSVG(u)
	if err != nil {
		http.Error(w, "cannot encode QR code", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(svg)
}

// ownURL reports whether raw is an HTTPS URL of this server: https scheme,
// one of its hosts, its HTTPS port, and either the root path, the sign-up
// path or an invite path, with no credentials, query or fragment. QR codes are never drawn for
// anything else, so the endpoint cannot be used to brand phishing links.
func (s *server) ownURL(raw string) bool {
	if raw == "" || len(raw) > maxQRURL {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return false
	}
	if u.Port() != strconv.Itoa(s.opts.HTTPSPort) {
		return false
	}
	switch p := u.EscapedPath(); {
	case p == "" || p == "/" || p == "/register":
	case strings.HasPrefix(p, "/invite/") && inviteCode.MatchString(strings.TrimPrefix(p, "/invite/")):
	default:
		return false
	}
	return s.knownHost(u.Hostname())
}

func (s *server) knownHost(host string) bool {
	want := canonicalHost(host)
	if want == "" {
		return false
	}
	var hosts []string
	for _, base := range s.urls() {
		if u, err := url.Parse(base); err == nil {
			hosts = append(hosts, u.Hostname())
		}
	}
	if s.opts.Hosts != nil {
		hosts = append(hosts, s.opts.Hosts()...)
	}
	for _, h := range hosts {
		if canonicalHost(h) == want {
			return true
		}
	}
	return false
}

// canonicalHost lower-cases names and normalizes IP literals; it returns ""
// for anything that is neither.
func canonicalHost(h string) string {
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Unmap().WithZone("").String()
	}
	if len(h) > 253 || !hostName.MatchString(h) {
		return ""
	}
	return strings.ToLower(h)
}

// handleRedirect sends every other request to the same path on an HTTPS URL
// of this server (308 keeps the method and body). The Host header is reused
// only when it names this server (certificate SANs or advertised URLs); any
// other Host, including one given in an absolute-form request target, goes to
// the first advertised HTTPS URL so a forged Host cannot steer the client.
func (s *server) handleRedirect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	path := r.URL.RequestURI()
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if s.knownHost(host) {
		target := "https://" + net.JoinHostPort(canonicalHost(host), strconv.Itoa(s.opts.HTTPSPort)) + path
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
		return
	}
	advertised := s.urls()
	if len(advertised) == 0 {
		http.Error(w, "missing or invalid Host header", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, strings.TrimSuffix(advertised[0], "/")+path, http.StatusPermanentRedirect)
}
