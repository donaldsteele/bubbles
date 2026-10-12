package httpapi

import (
	"net/http"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func (a *api) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.chat.ListUsers(r.Context(), actorFrom(r), r.URL.Query().Get("q"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.UserList{Items: users})
}

func (a *api) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := a.chat.GetUser(r.Context(), actorFrom(r), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}
