package netutil

import (
	"net"
	"net/http"
	"net/netip"
)

const forbiddenBody = `{"error":{"code":"forbidden","message":"requests from this network address are not allowed"}}` + "\n"

// Guard returns middleware that answers 403 to any request whose source
// address is neither private (see IsPrivate) nor inside one of allowed.
func Guard(allowed []netip.Prefix) func(http.Handler) http.Handler {
	return GuardFunc(func() []netip.Prefix { return allowed })
}

// GuardFunc is Guard with an allow-list that is looked up per request, for
// lists that an administrator can edit while the server runs.
func GuardFunc(allowed func() []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !sourceAllowed(r.RemoteAddr, allowed()) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(forbiddenBody))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// sourceAllowed fails closed: an unparseable remote address is not allowed.
func sourceAllowed(remote string, allowed []netip.Prefix) bool {
	addr, ok := parseRemote(remote)
	if !ok {
		return false
	}
	if IsPrivate(addr) {
		return true
	}
	for _, p := range allowed {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func parseRemote(remote string) (netip.Addr, bool) {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return normalize(addr), true
}
