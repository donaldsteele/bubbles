package httpapi

import (
	"errors"
	"net/http"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/push"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// registerPushRoutes adds the Web Push endpoints. All need a signed-in user
// and act on that user's subscriptions only.
func registerPushRoutes(mux *http.ServeMux, d Deps) {
	a := &api{auth: d.Auth, logger: d.Logger}
	_, private := routeKinds(d)
	p := pushAPI{api: a, push: d.Push}
	mux.Handle("GET "+apiPrefix+"/push/vapid-public-key", private(p.vapidKey))
	mux.Handle("POST "+apiPrefix+"/push/subscriptions", private(p.subscribe))
	mux.Handle("DELETE "+apiPrefix+"/push/subscriptions", private(p.unsubscribe))
}

type pushAPI struct {
	*api
	push *push.Service
}

// vapidKey answers GET /push/vapid-public-key: 404 while the admin has not
// enabled web push, so a client that gets one knows not to offer
// notifications.
func (p pushAPI) vapidKey(w http.ResponseWriter, _ *http.Request) {
	if !p.push.Enabled() {
		WriteError(w, http.StatusNotFound, CodeNotFound, "web push is not enabled")
		return
	}
	writeJSON(w, http.StatusOK, protocol.PushVapidKey{Key: p.push.PublicKey()})
}

// subscribe answers POST /push/subscriptions. The endpoint is a URL the server
// will POST to, so the push service decides what is acceptable (an https URL
// of a known browser push service) and everything else is a 400 on `endpoint`.
func (p pushAPI) subscribe(w http.ResponseWriter, r *http.Request) {
	var req protocol.PushSubscribeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	err := p.push.Subscribe(r.Context(), auth.UserFrom(r.Context()).ID, push.SubscribeInput{
		Endpoint: req.Endpoint, P256DH: req.Keys.P256DH, Auth: req.Keys.Auth, UserAgent: r.UserAgent(),
	})
	if err != nil {
		p.failPush(w, r, err)
		return
	}
	noContent(w)
}

// unsubscribe answers DELETE /push/subscriptions. It is idempotent and only
// ever touches the caller's own subscriptions.
func (p pushAPI) unsubscribe(w http.ResponseWriter, r *http.Request) {
	var req protocol.PushUnsubscribeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Endpoint == "" || len(req.Endpoint) > push.MaxEndpointLen {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid",
			map[string]string{"endpoint": "is required and at most 2048 characters"})
		return
	}
	if err := p.push.Unsubscribe(r.Context(), auth.UserFrom(r.Context()).ID, req.Endpoint); err != nil {
		p.fail(w, r, err)
		return
	}
	noContent(w)
}

func (p pushAPI) failPush(w http.ResponseWriter, r *http.Request, err error) {
	var ve *push.ValidationError
	switch {
	case errors.As(err, &ve):
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid", ve.Fields)
	case errors.Is(err, push.ErrTooManySubscriptions):
		WriteError(w, http.StatusConflict, CodeConflict, "too many push subscriptions; remove one first (at most 10 per user)")
	default:
		p.fail(w, r, err)
	}
}
