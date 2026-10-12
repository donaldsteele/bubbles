package httpapi

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"

	"github.com/donaldsteele/bubbles/internal/apps"
	"github.com/donaldsteele/bubbles/internal/hooks"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// hooksPrefix is where incoming webhooks are served, outside /api/v1.
const hooksPrefix = "/hooks"

// registerHookRoutes adds the incoming-webhook endpoint and its admin API.
// POST /hooks/{token} has no authentication besides the secret in its path,
// is not subject to the Origin check (senders are not browsers) and is
// limited per webhook; the admin endpoints are for an admin's browser session.
func registerHookRoutes(mux *http.ServeMux, d Deps) {
	h := webhookAPI{api: &api{auth: d.Auth, logger: d.Logger}, hooks: d.Hooks, endpointFn: d.Endpoint}
	admin := adminRoute(d)

	mux.HandleFunc("POST "+hooksPrefix+"/{token}", h.ingest)

	mux.Handle("GET "+apiPrefix+"/admin/webhooks", admin(h.list))
	mux.Handle("POST "+apiPrefix+"/admin/webhooks", admin(h.create))
	mux.Handle("DELETE "+apiPrefix+"/admin/webhooks/{id}", admin(h.revoke))
}

type webhookAPI struct {
	*api
	hooks      *hooks.Service
	endpointFn func() apps.Endpoint
}

// url is the address a sender posts to: the server's first LAN host and HTTPS
// port, else the host the admin used.
func (h webhookAPI) url(r *http.Request, token string) string {
	ep := appAPI{api: h.api, endpointFn: h.endpointFn}.endpoint(r)
	host := ep.Host
	if ep.Port > 0 {
		host = net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port))
	}
	return "https://" + host + hooksPrefix + "/" + token
}

func (h webhookAPI) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.hooks.List(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.WebhookList{Items: items})
}

// create answers POST /admin/webhooks. The URL, which carries the secret, is
// in this answer only.
func (h webhookAPI) create(w http.ResponseWriter, r *http.Request) {
	var req protocol.CreateWebhookRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	wh, token, err := h.hooks.Create(r.Context(), actorFrom(r), auditActorFrom(r), hooks.CreateInput{
		ChannelID: req.ChannelID, Name: req.Name,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, protocol.CreatedWebhook{Webhook: wh, URL: h.url(r, token)})
}

func (h webhookAPI) revoke(w http.ResponseWriter, r *http.Request) {
	if err := h.hooks.Revoke(r.Context(), auditActorFrom(r), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	noContent(w)
}

// ingest answers POST /hooks/{token}: 200 "ok" as plain text, like Slack. An
// unknown or revoked token is a 404 before anything else is read.
func (h webhookAPI) ingest(w http.ResponseWriter, r *http.Request) {
	target, err := h.hooks.Lookup(r.Context(), r.PathValue("token"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if ok, wait := h.hooks.Allow(target.ID); !ok {
		WriteRateLimited(w, wait)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, hooks.MaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			WriteError(w, http.StatusRequestEntityTooLarge, CodeTooLarge, "request body is larger than 256 KiB")
		} else {
			WriteError(w, http.StatusBadRequest, CodeBadRequest, "request body could not be read")
		}
		return
	}
	doc, err := hooks.DecodeBody(r.Header.Get("Content-Type"), body)
	if errors.Is(err, hooks.ErrUnsupportedMedia) {
		WriteError(w, http.StatusUnsupportedMediaType, CodeUnsupportedMedia, err.Error())
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	msg, err := hooks.Translate(doc)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if _, err := h.hooks.Deliver(r.Context(), target, msg); err != nil {
		h.fail(w, r, err)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "text/plain; charset=utf-8")
	hd.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}
