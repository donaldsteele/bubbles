package httpapi

import (
	"net/http"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// createPairing answers POST /pairing/requests. The poll secret in the
// answer is shown once.
func (h appAPI) createPairing(w http.ResponseWriter, r *http.Request) {
	var in protocol.PairingRequestInput
	if !decodeJSON(w, r, &in) {
		return
	}
	res, err := h.apps.CreatePairing(r.Context(), clientIP(r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// pollPairing answers GET /pairing/requests/{id}. The token is in the answer
// of exactly one poll: the first one after the admin approved. A wrong or
// missing X-Poll-Secret is 404, whatever the request's state.
func (h appAPI) pollPairing(w http.ResponseWriter, r *http.Request) {
	st, err := h.apps.PollPairing(r.Context(), clientIP(r), r.PathValue("id"), r.Header.Get(protocol.PairingHeader))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// cancelPairing answers DELETE /pairing/requests/{id}: the app withdraws its
// request with the poll secret in X-Poll-Secret. A wrong or missing secret,
// or an unknown id, is 404 exactly like the poll.
func (h appAPI) cancelPairing(w http.ResponseWriter, r *http.Request) {
	if err := h.apps.CancelPairing(r.Context(), clientIP(r), r.PathValue("id"), r.Header.Get(protocol.PairingHeader)); err != nil {
		h.fail(w, r, err)
		return
	}
	noContent(w)
}
