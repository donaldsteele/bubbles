package httpapi

import (
	"errors"
	"net/http"

	"github.com/donaldsteele/bubbles/internal/apps"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// registerCommandRoutes adds the slash-command endpoints: an app registers
// its commands and answers invocations; a person lists commands and runs them.
// Invocations draw from the message-post limiter shared with REST and MCP posts.
func registerCommandRoutes(mux *http.ServeMux, d Deps, lim limits) {
	h := commandAPI{api: &api{
		auth: d.Auth, logger: d.Logger,
		posts: lim.posts,
	}, apps: d.Apps}
	route := appRoute(d)
	_, human := routeKinds(d)
	mux.Handle("PUT "+apiPrefix+"/apps/self/commands", route(h.setCommands))
	mux.Handle("GET "+apiPrefix+"/commands", route(h.list))
	mux.Handle("POST "+apiPrefix+"/commands/invoke", human(h.invoke))
	mux.Handle("POST "+apiPrefix+"/commands/invocations/{id}/respond", route(h.respond))
}

type commandAPI struct {
	*api
	apps *apps.Service
}

// grant returns the app token behind the request, or writes 403 when the
// caller is a browser session.
func commandGrant(w http.ResponseWriter, r *http.Request) (*auth.TokenGrant, bool) {
	p := auth.PrincipalFrom(r.Context())
	if p == nil || p.Token == nil {
		WriteError(w, http.StatusForbidden, CodeForbidden, "only an app token can use this endpoint")
		return nil, false
	}
	return p.Token, true
}

func (h commandAPI) setCommands(w http.ResponseWriter, r *http.Request) {
	g, ok := commandGrant(w, r)
	if !ok {
		return
	}
	var in []protocol.CommandInput
	if !decodeJSON(w, r, &in) {
		return
	}
	items, err := h.apps.SetCommands(r.Context(), g, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.CommandList{Items: items})
}

func (h commandAPI) list(w http.ResponseWriter, r *http.Request) {
	if p := auth.PrincipalFrom(r.Context()); p.Token != nil && !apps.HasScope(p.Token.Scopes, apps.ScopeMessagesRead) {
		WriteError(w, http.StatusForbidden, CodeForbidden, "this token lacks the messages:read scope")
		return
	}
	items, err := h.apps.ListCommands(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.CommandList{Items: items})
}

func (h commandAPI) invoke(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r)
	if ok, wait := h.posts.Allow(actor.ID); !ok {
		WriteRateLimited(w, wait)
		return
	}
	var req protocol.InvokeCommandRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := h.apps.Invoke(r.Context(), actor, req)
	var pending *apps.PendingLimitError
	switch {
	case errors.Is(err, apps.ErrAppOffline):
		WriteError(w, http.StatusServiceUnavailable, CodeUnavailable, "the app is offline")
	case errors.As(err, &pending):
		WriteRateLimited(w, pending.RetryAfter)
	case err != nil:
		h.fail(w, r, err)
	case res.Builtin:
		writeJSON(w, http.StatusOK, res)
	default:
		writeJSON(w, http.StatusAccepted, res)
	}
}

func (h commandAPI) respond(w http.ResponseWriter, r *http.Request) {
	g, ok := commandGrant(w, r)
	if !ok {
		return
	}
	var req protocol.RespondCommandRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := h.apps.Respond(r.Context(), g, r.PathValue("id"), req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
