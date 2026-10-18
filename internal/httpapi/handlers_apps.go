package httpapi

import (
	"net"
	"net/http"
	"strconv"

	"github.com/donaldsteele/bubbles/internal/apps"
	"github.com/donaldsteele/bubbles/internal/auth"
)

// registerAppRoutes adds GET /apps/self, DELETE /apps/self/token, the pairing
// endpoints and the admin API for apps, tokens and pairing requests.
func registerAppRoutes(mux *http.ServeMux, d Deps) {
	h := appAPI{api: &api{auth: d.Auth, logger: d.Logger}, apps: d.Apps, endpointFn: d.Endpoint}
	public, _ := routeKinds(d)
	app := appRoute(d)
	admin := adminRoute(d)

	mux.Handle("GET "+apiPrefix+"/apps/self", app(h.self))
	mux.Handle("DELETE "+apiPrefix+"/apps/self/token", app(h.revokeSelf))

	// Pairing is for an app that has no credentials yet: anonymous, LAN-guarded,
	// limited per source address.
	mux.Handle("POST "+apiPrefix+"/pairing/requests", public(h.createPairing))
	mux.Handle("GET "+apiPrefix+"/pairing/requests/{id}", public(h.pollPairing))
	mux.Handle("DELETE "+apiPrefix+"/pairing/requests/{id}", public(h.cancelPairing))

	mux.Handle("GET "+apiPrefix+"/admin/apps", admin(h.listApps))
	mux.Handle("POST "+apiPrefix+"/admin/apps", admin(h.createApp))
	mux.Handle("PATCH "+apiPrefix+"/admin/apps/{id}", admin(h.updateApp))
	mux.Handle("POST "+apiPrefix+"/admin/apps/{id}/tokens", admin(h.issueToken))
	mux.Handle("DELETE "+apiPrefix+"/admin/apps/{id}/tokens/{token_id}", admin(h.revokeToken))
	mux.Handle("GET "+apiPrefix+"/admin/pairing-requests", admin(h.listPairing))
	mux.Handle("POST "+apiPrefix+"/admin/pairing-requests/{id}/approve", admin(h.approvePairing))
	mux.Handle("POST "+apiPrefix+"/admin/pairing-requests/{id}/deny", admin(h.denyPairing))
}

// adminRoute wraps an admin endpoint: a browser session of an admin, with the
// Origin check and the per-session rate limit. App tokens are refused.
func adminRoute(d Deps) func(http.HandlerFunc) http.Handler {
	authn := auth.Middleware(authenticators(d)...)
	return func(h http.HandlerFunc) http.Handler {
		return chain(h, authn, humansOnly, originCheck, func(next http.Handler) http.Handler {
			return requireUser(d.Auth.APILimiter(), nil, adminOnly(next))
		})
	}
}

type appAPI struct {
	*api
	apps       *apps.Service
	endpointFn func() apps.Endpoint
}

// endpoint says how clients reach the server: the configured endpoint, else
// the host the request was sent to.
func (h appAPI) endpoint(r *http.Request) apps.Endpoint {
	if h.endpointFn != nil {
		return h.endpointFn()
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		return apps.Endpoint{Host: r.Host, Port: 443}
	}
	n, _ := strconv.Atoi(port)
	return apps.Endpoint{Host: host, Port: n}
}

// self answers GET /apps/self: the app and token behind the bearer token.
func (h appAPI) self(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r.Context())
	if p.Token == nil {
		WriteError(w, http.StatusForbidden, CodeForbidden, "only an app token can ask for its app")
		return
	}
	self, err := h.apps.Self(r.Context(), p.Token.AppID, p.Token.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, self)
}

// revokeSelf answers DELETE /apps/self/token: the calling app token revokes
// itself (an app's "Disconnect"). Its next request is 401. A browser session
// gets 403 like GET /apps/self.
func (h appAPI) revokeSelf(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r.Context())
	if p.Token == nil {
		WriteError(w, http.StatusForbidden, CodeForbidden, "only an app token can revoke itself")
		return
	}
	if err := h.apps.RevokeToken(r.Context(), auditActorFrom(r), p.Token.AppID, p.Token.ID); err != nil {
		h.fail(w, r, err)
		return
	}
	noContent(w)
}
