package httpapi

import (
	"net/http"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// putReaction and deleteReaction take the emoji from the URL-decoded path
// segment, so multi-codepoint emoji (ZWJ sequences, skin tones, flags) arrive
// byte for byte as the client encoded them.
func (a *api) putReaction(w http.ResponseWriter, r *http.Request) {
	rs, err := a.chat.AddReaction(r.Context(), actorFrom(r), r.PathValue("id"), r.PathValue("emoji"))
	a.writeReactions(w, r, rs, err)
}

func (a *api) deleteReaction(w http.ResponseWriter, r *http.Request) {
	rs, err := a.chat.RemoveReaction(r.Context(), actorFrom(r), r.PathValue("id"), r.PathValue("emoji"))
	a.writeReactions(w, r, rs, err)
}

func (a *api) writeReactions(w http.ResponseWriter, r *http.Request, rs []protocol.Reaction, err error) {
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if rs == nil {
		rs = []protocol.Reaction{}
	}
	writeJSON(w, http.StatusOK, protocol.ReactionList{Items: rs})
}
