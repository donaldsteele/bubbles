package httpapi

import (
	"net/http"
	"strings"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func (a *api) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, meToProtocol(auth.UserFrom(r.Context())))
}

// patchMe answers PATCH /me: a partial update of the caller's own profile.
func (a *api) patchMe(w http.ResponseWriter, r *http.Request) {
	var req protocol.UpdateMeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in, err := profileInput(req)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	u, err := a.auth.UpdateProfile(r.Context(), auth.UserFrom(r.Context()).ID, in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, meToProtocol(u))
}

// profileInput maps the request to the service input. display_name and prefs
// cannot be cleared; status_text null empties the text.
func profileInput(req protocol.UpdateMeRequest) (auth.ProfileInput, error) {
	var in auth.ProfileInput
	fields := map[string]string{}
	if req.DisplayName.Set {
		if req.DisplayName.Value == nil {
			fields["display_name"] = "must not be null"
		}
		in.DisplayName = req.DisplayName.Value
	}
	if req.StatusText.Set {
		in.StatusText = req.StatusText.Value
		if in.StatusText == nil {
			in.StatusText = new(string)
		}
	}
	if req.AvatarFileID.Set {
		in.SetAvatar, in.AvatarFileID = true, req.AvatarFileID.Value
	}
	if req.Prefs.Set {
		if req.Prefs.Value == nil {
			fields["prefs"] = "must not be null"
		} else {
			in.Prefs = *req.Prefs.Value
		}
	}
	if len(fields) > 0 {
		return in, &auth.ValidationError{Fields: fields}
	}
	return in, nil
}

func (a *api) changePassword(w http.ResponseWriter, r *http.Request) {
	var req protocol.ChangePasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := auth.PrincipalFrom(r.Context())
	in := auth.ChangePasswordInput{UserID: p.User.ID, Current: req.Current, New: req.New, IP: clientIP(r)}
	if p.Session != nil {
		in.KeepSession = p.Session.IDHash
	}
	if err := a.auth.ChangePassword(r.Context(), in); err != nil {
		a.fail(w, r, err)
		return
	}
	noContent(w)
}

func (a *api) listSessions(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r.Context())
	list, err := a.auth.ListSessions(r.Context(), p.User.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := protocol.SessionList{Items: make([]protocol.Session, 0, len(list))}
	for _, s := range list {
		out.Items = append(out.Items, protocol.Session{
			ID: s.ID, Kind: s.Kind, Current: p.Session != nil && s.ID == p.Session.ID,
			CreatedAt:   protocol.FormatTime(s.CreatedAt),
			LastSeenAt:  protocol.FormatTime(s.LastSeenAt),
			IdleExpires: protocol.FormatTime(s.IdleExpiresAt),
			AbsExpires:  protocol.FormatTime(s.AbsExpiresAt),
			UserAgent:   s.UserAgent, IP: s.IP,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) revokeSession(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r.Context())
	id := r.PathValue("id")
	if err := a.auth.RevokeSession(r.Context(), audit.User(p.User.ID, clientIP(r)), p.User.ID, id); err != nil {
		a.fail(w, r, err)
		return
	}
	if p.Session != nil && strings.EqualFold(p.Session.ID, id) {
		http.SetCookie(w, auth.ClearCookie())
	}
	noContent(w)
}
