package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/donaldsteele/bubbles/internal/auth"
)

// originMatches reports whether raw (an Origin or Referer value) names the
// host the request was sent to. "null" origins (sandboxed frames, some
// privacy modes) never match. Behind TLS the scheme must be https.
func originMatches(raw string, r *http.Request) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if r.TLS != nil && u.Scheme != "https" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// browserOrigin returns the Origin header, or the Referer when there is no
// Origin, and whether either was sent.
func browserOrigin(r *http.Request) (string, bool) {
	if o := r.Header.Get("Origin"); o != "" {
		return o, true
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		return ref, true
	}
	return "", false
}

// SameOrigin reports whether r carries an Origin (or Referer) that matches
// its Host. The WebSocket upgrade uses it too.
func SameOrigin(r *http.Request) bool {
	raw, ok := browserOrigin(r)
	return ok && originMatches(raw, r)
}

// originCheck is the CSRF defense for state-changing requests (anything but
// GET, HEAD and OPTIONS). A cookie-authenticated request must carry an
// Origin (or Referer) that matches the request host. Requests without a
// cookie session (login, setup, bearer-token clients) are only rejected when
// a browser did send a mismatching Origin, which blocks login CSRF.
// It must run after the auth middleware.
func originCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if p := auth.PrincipalFrom(r.Context()); p != nil && p.Via != auth.ViaCookie {
			next.ServeHTTP(w, r)
			return
		}
		raw, sent := browserOrigin(r)
		cookieAuthed := auth.PrincipalFrom(r.Context()) != nil
		if (sent && !originMatches(raw, r)) || (!sent && cookieAuthed) {
			WriteError(w, http.StatusForbidden, CodeForbidden, "cross-origin request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}
