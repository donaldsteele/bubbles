package httpapi

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// decodeOptionalJSON is decodeJSON for a body that may be left out entirely.
// A chunked request (ContentLength -1) carries no length to inspect, so the
// first byte is peeked: a body with zero bytes counts as absent, and any byte
// hands the untouched stream to decodeJSON.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.ContentLength == 0 || r.Body == http.NoBody {
		return true
	}
	br := bufio.NewReader(r.Body)
	if _, err := br.Peek(1); errors.Is(err, io.EOF) {
		return true
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{br, r.Body}
	return decodeJSON(w, r, dst)
}

func (a *api) listChannels(w http.ResponseWriter, r *http.Request) {
	list, err := a.chat.ListChannels(r.Context(), actorFrom(r), r.URL.Query().Get("scope"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.ChannelList{Items: list})
}

func (a *api) createChannel(w http.ResponseWriter, r *http.Request) {
	var req protocol.CreateChannelRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ch, err := a.chat.CreateChannel(r.Context(), actorFrom(r), chat.CreateChannelParams{
		Name: req.Name, Kind: req.Kind, Topic: req.Topic, MemberIDs: req.MemberIDs,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, ch)
}

func (a *api) updateChannel(w http.ResponseWriter, r *http.Request) {
	var req protocol.UpdateChannelRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := chat.UpdateChannelParams{Name: req.Name, Topic: req.Topic, Description: req.Description}
	if req.RetentionDays.Set {
		p.RetentionDays = chat.Nullable[int]{Set: true, Value: req.RetentionDays.Value}
	}
	ch, err := a.chat.UpdateChannel(r.Context(), actorFrom(r), r.PathValue("id"), p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (a *api) archiveChannel(w http.ResponseWriter, r *http.Request) {
	ch, err := a.chat.ArchiveChannel(r.Context(), actorFrom(r), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (a *api) unarchiveChannel(w http.ResponseWriter, r *http.Request) {
	ch, err := a.chat.UnarchiveChannel(r.Context(), actorFrom(r), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (a *api) joinChannel(w http.ResponseWriter, r *http.Request) {
	m, err := a.chat.JoinChannel(r.Context(), actorFrom(r), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (a *api) leaveChannel(w http.ResponseWriter, r *http.Request) {
	if err := a.chat.LeaveChannel(r.Context(), actorFrom(r), r.PathValue("id")); err != nil {
		a.fail(w, r, err)
		return
	}
	noContent(w)
}

func (a *api) listMembers(w http.ResponseWriter, r *http.Request) {
	list, err := a.chat.ListMembers(r.Context(), actorFrom(r), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.MemberList{Items: list})
}

func (a *api) putMember(w http.ResponseWriter, r *http.Request) {
	var req protocol.AddMemberRequest
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	m, err := a.chat.AddMember(r.Context(), actorFrom(r), r.PathValue("id"), r.PathValue("user_id"), req.Role)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (a *api) deleteMember(w http.ResponseWriter, r *http.Request) {
	if err := a.chat.RemoveMember(r.Context(), actorFrom(r), r.PathValue("id"), r.PathValue("user_id")); err != nil {
		a.fail(w, r, err)
		return
	}
	noContent(w)
}

func (a *api) updateMembership(w http.ResponseWriter, r *http.Request) {
	var req protocol.UpdateMembershipRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u := chat.MembershipUpdate{NotifyLevel: req.NotifyLevel, AlertMinSeverity: req.AlertMinSeverity}
	if req.MutedUntil.Set {
		if req.MutedUntil.Value == nil {
			u.MutedUntil = chat.Clear[time.Time]()
		} else {
			until, err := time.Parse(time.RFC3339, *req.MutedUntil.Value)
			if err != nil {
				WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid",
					map[string]string{"muted_until": "must be an RFC 3339 timestamp or null"})
				return
			}
			u.MutedUntil = chat.Some(until)
		}
	}
	m, err := a.chat.UpdateMembership(r.Context(), actorFrom(r), r.PathValue("id"), u)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (a *api) markRead(w http.ResponseWriter, r *http.Request) {
	var req protocol.MarkReadRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.MessageID == "" {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid",
			map[string]string{"message_id": "required"})
		return
	}
	ev, err := a.chat.MarkRead(r.Context(), actorFrom(r), r.PathValue("id"), req.MessageID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

// startDM answers 201 when the conversation was created and 200 when it
// already existed.
func (a *api) startDM(w http.ResponseWriter, r *http.Request) {
	var req protocol.StartDMRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.UserIDs == nil {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid",
			map[string]string{"user_ids": "required"})
		return
	}
	ch, created, err := a.chat.StartDM(r.Context(), actorFrom(r), req.UserIDs)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, ch)
}
