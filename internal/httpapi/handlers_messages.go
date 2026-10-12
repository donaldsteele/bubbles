package httpapi

import (
	"net/http"

	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func (a *api) listMessages(w http.ResponseWriter, r *http.Request) {
	opts, ok := parsePage(w, r)
	if !ok {
		return
	}
	page, err := a.chat.ListMessages(r.Context(), actorFrom(r), r.PathValue("id"), opts)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *api) listThread(w http.ResponseWriter, r *http.Request) {
	opts, ok := parsePage(w, r)
	if !ok {
		return
	}
	th, err := a.chat.ListThread(r.Context(), actorFrom(r), r.PathValue("id"), opts)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, th)
}

// postMessage answers 201 for a new message and 200 when the nonce matched an
// earlier post, which is returned unchanged. Posts are limited per user on top
// of the per-session limit.
func (a *api) postMessage(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r)
	if ok, wait := a.posts.Allow(actor.ID); !ok {
		WriteRateLimited(w, wait)
		return
	}
	var req protocol.CreateMessageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := chat.CreateMessageParams{ChannelID: r.PathValue("id"), Body: req.Body, FileIDs: req.FileIDs, Nonce: req.Nonce}
	if req.ThreadRootID != nil {
		p.ThreadRootID = *req.ThreadRootID
	}
	msg, created, err := a.chat.CreateMessage(r.Context(), actor, p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, msg)
}

func (a *api) editMessage(w http.ResponseWriter, r *http.Request) {
	var req protocol.EditMessageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	msg, err := a.chat.EditMessage(r.Context(), actorFrom(r), r.PathValue("id"), req.Body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

// deleteMessage leaves the permission decision (author, channel owner, admin
// moderation) entirely to the chat service.
func (a *api) deleteMessage(w http.ResponseWriter, r *http.Request) {
	if err := a.chat.DeleteMessage(r.Context(), actorFrom(r), r.PathValue("id")); err != nil {
		a.fail(w, r, err)
		return
	}
	noContent(w)
}
