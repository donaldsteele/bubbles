package httpapi

import (
	"net/http"
	"time"

	"github.com/donaldsteele/bubbles/internal/apps"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func (h appAPI) listApps(w http.ResponseWriter, r *http.Request) {
	items, err := h.apps.ListApps(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.AppList{Items: items})
}

func (h appAPI) createApp(w http.ResponseWriter, r *http.Request) {
	var req protocol.CreateAppRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	app, err := h.apps.CreateApp(r.Context(), auditActorFrom(r), apps.CreateAppInput{
		Name: req.Name, Description: req.Description, Username: req.Username, OwnerID: actorFrom(r).ID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, app)
}

func (h appAPI) updateApp(w http.ResponseWriter, r *http.Request) {
	var req protocol.UpdateAppRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	app, err := h.apps.UpdateApp(r.Context(), auditActorFrom(r), r.PathValue("id"), apps.UpdateAppInput{
		Name: req.Name, Description: req.Description, Disabled: req.Disabled,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

// issueToken answers POST /admin/apps/{id}/tokens. The secret and the
// connection string are in this answer only.
func (h appAPI) issueToken(w http.ResponseWriter, r *http.Request) {
	var req protocol.CreateAppTokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in := apps.IssueTokenInput{Scopes: req.Scopes, ChannelIDs: req.ChannelIDs}
	if req.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339Nano, *req.ExpiresAt)
		if err != nil {
			WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid",
				map[string]string{"expires_at": "expires_at must be an RFC 3339 timestamp"})
			return
		}
		in.ExpiresAt = &t
	}
	tok, err := h.apps.IssueToken(r.Context(), auditActorFrom(r), r.PathValue("id"), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, protocol.IssuedAppToken{
		Token: tok.Secret, ConnectionString: h.endpoint(r).ConnectionString(tok.Secret), Info: tok.Info,
	})
}

func (h appAPI) revokeToken(w http.ResponseWriter, r *http.Request) {
	if err := h.apps.RevokeToken(r.Context(), auditActorFrom(r), r.PathValue("id"), r.PathValue("token_id")); err != nil {
		h.fail(w, r, err)
		return
	}
	noContent(w)
}

// listPairing answers GET /admin/pairing-requests: the open requests, each
// with the CA fingerprint the admin compares with the one the app shows.
func (h appAPI) listPairing(w http.ResponseWriter, r *http.Request) {
	items, err := h.apps.ListPending(r.Context(), h.endpoint(r).Fingerprint)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.PendingPairingList{Items: items})
}

func (h appAPI) approvePairing(w http.ResponseWriter, r *http.Request) {
	var req protocol.ApprovePairingRequest
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	res, err := h.apps.ApprovePairing(r.Context(), actorFrom(r), auditActorFrom(r), r.PathValue("id"), req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h appAPI) denyPairing(w http.ResponseWriter, r *http.Request) {
	if err := h.apps.DenyPairing(r.Context(), auditActorFrom(r), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	noContent(w)
}
