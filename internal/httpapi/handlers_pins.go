package httpapi

import (
	"net/http"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func (a *api) listPins(w http.ResponseWriter, r *http.Request) {
	pins, err := a.chat.ListPins(r.Context(), actorFrom(r), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.PinList{Items: pins})
}

func (a *api) putPin(w http.ResponseWriter, r *http.Request) {
	pin, err := a.chat.PinMessage(r.Context(), actorFrom(r), r.PathValue("id"), r.PathValue("message_id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pin)
}

func (a *api) deletePin(w http.ResponseWriter, r *http.Request) {
	if err := a.chat.UnpinMessage(r.Context(), actorFrom(r), r.PathValue("id"), r.PathValue("message_id")); err != nil {
		a.fail(w, r, err)
		return
	}
	noContent(w)
}
