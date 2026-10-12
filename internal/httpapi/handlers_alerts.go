package httpapi

import (
	"net/http"
	"strconv"

	"github.com/donaldsteele/bubbles/internal/alerts"
	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Alert ingest is limited per app to 60 per minute with a burst of 20, on top
// of the per-token request limit.
const (
	alertsPerSecond = 1
	alertBurst      = 20
)

// registerAlertRoutes adds the /alerts endpoints. They accept a browser
// session (acknowledge, resolve, list) and an app token (post, resolve by
// key, list), as the alerts service allows.
func registerAlertRoutes(mux *http.ServeMux, d Deps, lim limits) {
	h := alertAPI{
		api:    &api{auth: d.Auth, logger: d.Logger},
		alerts: d.Alerts,
		limit:  lim.alerts,
	}
	route := appRoute(d)
	mux.Handle("POST "+apiPrefix+"/alerts", route(h.post))
	mux.Handle("POST "+apiPrefix+"/alerts/resolve", route(h.resolveByKey))
	mux.Handle("POST "+apiPrefix+"/alerts/{id}/ack", route(h.ack))
	mux.Handle("POST "+apiPrefix+"/alerts/{id}/resolve", route(h.resolve))
	mux.Handle("GET "+apiPrefix+"/alerts", route(h.list))
}

type alertAPI struct {
	*api
	alerts *alerts.Service
	// limit is the per-app ingest limit, keyed by the bot's user id.
	limit *auth.BucketLimiter
}

// auditActorFrom is the audit actor of the authenticated caller.
func auditActorFrom(r *http.Request) audit.Actor {
	u := auth.UserFrom(r.Context())
	kind := audit.KindUser
	if u.Kind == protocol.KindApp {
		kind = audit.KindApp
	}
	return audit.Actor{ID: u.ID, Kind: kind, IP: clientIP(r)}
}

// post answers POST /alerts: 201 for a new alert, 200 for a repeat of an
// open one.
func (h alertAPI) post(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r)
	if ok, wait := h.limit.Allow(actor.ID); !ok {
		WriteRateLimited(w, wait)
		return
	}
	var in protocol.AlertInput
	if !decodeJSON(w, r, &in) {
		return
	}
	res, err := h.alerts.Post(r.Context(), actor, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if res.Created() {
		status = http.StatusCreated
	}
	writeJSON(w, status, protocol.PostedAlert{Message: res.Message, Transition: res.Transition})
}

func (h alertAPI) resolveByKey(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r)
	if ok, wait := h.limit.Allow(actor.ID); !ok {
		WriteRateLimited(w, wait)
		return
	}
	var req protocol.ResolveAlertByKeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	msg, err := h.alerts.ResolveByKey(r.Context(), actor, auditActorFrom(r), req.DedupeKey, req.Text)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

func (h alertAPI) ack(w http.ResponseWriter, r *http.Request) {
	msg, err := h.alerts.Ack(r.Context(), actorFrom(r), auditActorFrom(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

func (h alertAPI) resolve(w http.ResponseWriter, r *http.Request) {
	var req protocol.ResolveAlertRequest
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	msg, err := h.alerts.Resolve(r.Context(), actorFrom(r), auditActorFrom(r), r.PathValue("id"), req.Text)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

// list answers GET /alerts?state=&channel_id=&cursor=&limit=.
func (h alertAPI) list(w http.ResponseWriter, r *http.Request) {
	p, ok := parseAlertList(w, r)
	if !ok {
		return
	}
	page, err := h.alerts.List(r.Context(), actorFrom(r), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// parseAlertList reads the query of GET /alerts, each parameter at most once.
func parseAlertList(w http.ResponseWriter, r *http.Request) (alerts.ListParams, bool) {
	q := r.URL.Query()
	fields := map[string]string{}
	var p alerts.ListParams
	one := func(name string, dst *string, max int) {
		vals, present := q[name]
		switch {
		case !present:
		case len(vals) != 1:
			fields[name] = "give " + name + " at most once"
		case vals[0] == "" || len(vals[0]) > max:
			fields[name] = "is not valid"
		default:
			*dst = vals[0]
		}
	}
	one("state", &p.State, 16)
	one("channel_id", &p.ChannelID, maxAnchorLen)
	one("cursor", &p.Cursor, maxAnchorLen)
	if vals, present := q["limit"]; present {
		n, err := strconv.Atoi(vals[0])
		switch {
		case len(vals) != 1:
			fields["limit"] = "give limit at most once"
		case err != nil || n < 1 || n > chat.MaxPageLimit:
			fields["limit"] = "limit must be a whole number from 1 to " + strconv.Itoa(chat.MaxPageLimit)
		default:
			p.Limit = n
		}
	}
	if len(fields) > 0 {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "invalid alert list parameters", fields)
		return alerts.ListParams{}, false
	}
	return p, true
}
