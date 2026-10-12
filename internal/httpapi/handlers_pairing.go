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
