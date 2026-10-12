package httpapi

import (
	"net/http"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
)

// actorFrom builds the chat actor of the authenticated caller. It is the one
// place that maps a principal to chat permissions: a browser session carries
// the user's role, and a bearer app token adds its scopes and channel
// allowlist to the bot's kind.
func actorFrom(r *http.Request) chat.Actor {
	p := auth.PrincipalFrom(r.Context())
	a := chat.Actor{ID: p.User.ID, Role: string(p.User.Role), Kind: p.User.Kind}
	if p.Token != nil {
		a.Scopes, a.ChannelIDs = p.Token.Scopes, p.Token.ChannelIDs
	}
	return a
}

// bootstrap answers GET /bootstrap: the snapshot a client loads on start and
// after a resync, stamped with instance_id, epoch and seq_head.
func (a *api) bootstrap(w http.ResponseWriter, r *http.Request) {
	b, err := a.chat.Bootstrap(r.Context(), actorFrom(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}
