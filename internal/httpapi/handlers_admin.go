package httpapi

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/internal/tlsca"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// HostStatus is the live state of the server process that only the
// composition root knows.
type HostStatus struct {
	StartedAt      time.Time
	Connections    int
	HTTPSURLs      []string
	OnboardingURLs []string
	TLS            tlsca.Status
}

// AdminHost gives the admin endpoints the process-level state and actions.
type AdminHost interface {
	// Host reports the start time, open connections, URLs and certificate.
	Host() HostStatus
	// ReissueTLS re-issues the leaf certificate from the local CA
	// (tlsca.ErrBYO when the server uses its own certificate files).
	ReissueTLS() error
}

// maxAuditPage is the largest page GET /admin/audit returns.
const maxAuditPage = 100

// registerAdminRoutes adds the admin endpoints beyond apps, webhooks and
// reset: overview, settings, users, invites, channels, audit and TLS. All of
// them need an admin browser session.
func registerAdminRoutes(mux *http.ServeMux, d Deps) {
	h := adminAPI{api: &api{auth: d.Auth, logger: d.Logger}, deps: d}
	admin := adminRoute(d)
	p := apiPrefix + "/admin"
	mux.Handle("GET "+p+"/overview", admin(h.overview))
	mux.Handle("GET "+p+"/settings", admin(h.getSettings))
	mux.Handle("PATCH "+p+"/settings", admin(h.patchSettings))
	mux.Handle("POST "+p+"/registration", admin(h.registration))
	mux.Handle("GET "+p+"/users", admin(h.listUsers))
	mux.Handle("POST "+p+"/users", admin(h.createUser))
	mux.Handle("PATCH "+p+"/users/{id}", admin(h.updateUser))
	mux.Handle("POST "+p+"/users/{id}/password-reset", admin(h.passwordReset))
	mux.Handle("DELETE "+p+"/users/{id}/sessions", admin(h.revokeSessions))
	mux.Handle("GET "+p+"/invites", admin(h.listInvites))
	mux.Handle("POST "+p+"/invites", admin(h.createInvite))
	mux.Handle("DELETE "+p+"/invites/{id}", admin(h.revokeInvite))
	if d.Chat != nil {
		mux.Handle("GET "+p+"/channels", admin(h.listChannels))
		mux.Handle("PATCH "+p+"/channels/{id}", admin(h.updateChannel))
	}
	if d.DB != nil {
		mux.Handle("GET "+p+"/audit", admin(h.listAudit))
	}
	if d.Admin != nil && d.DB != nil {
		mux.Handle("GET "+p+"/tls", admin(h.getTLS))
		mux.Handle("POST "+p+"/tls/reissue", admin(h.reissueTLS))
	}
}

type adminAPI struct {
	*api
	deps Deps
}

// fail maps the admin-specific errors and defers to the common mapping.
func (h adminAPI) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrLastAdmin), errors.Is(err, auth.ErrSelfLockout):
		WriteError(w, http.StatusConflict, CodeConflict, err.Error())
	case errors.Is(err, tlsca.ErrBYO):
		WriteError(w, http.StatusConflict, CodeConflict, "the server uses --tls-cert/--tls-key, so there is no local CA to re-issue from")
	case errors.Is(err, audit.ErrBadCursor):
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid", map[string]string{"cursor": "invalid cursor"})
	default:
		h.api.fail(w, r, err)
	}
}

// baseURL is the https origin to put into links: the configured endpoint
// (first LAN address) so a link created on the server machine still works
// from other devices, else the host the admin used.
func (h adminAPI) baseURL(r *http.Request) string {
	if h.deps.Endpoint != nil {
		if e := h.deps.Endpoint(); e.Host != "" {
			return "https://" + net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
		}
	}
	return "https://" + r.Host
}

func (h adminAPI) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, err := h.auth.CountUsers(ctx)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := protocol.AdminOverview{
		InstanceName: h.auth.Settings().InstanceName(), Version: h.deps.Version, Users: users,
		HTTPSURLs: []string{}, OnboardingURLs: []string{},
	}
	if h.deps.Admin != nil {
		st := h.deps.Admin.Host()
		out.StartedAt = protocol.FormatTime(st.StartedAt.UnixMilli())
		out.UptimeSeconds = max(0, int64(h.deps.Now().Sub(st.StartedAt).Seconds()))
		out.Connections = st.Connections
		out.TLSMode = string(st.TLS.Mode)
		out.CAFingerprint = st.TLS.CAFingerprint
		out.CertNotAfter = protocol.FormatTime(st.TLS.LeafNotAfter.UnixMilli())
		out.HTTPSURLs = nonNil(st.HTTPSURLs)
		out.OnboardingURLs = nonNil(st.OnboardingURLs)
	}
	if h.deps.Files != nil {
		if out.StorageBytes, err = h.deps.Files.TotalBytes(ctx); err != nil {
			h.fail(w, r, err)
			return
		}
		if free, err := h.deps.Files.DiskFree(); err == nil {
			v := int64(min(free, 1<<62))
			out.DiskFreeBytes = &v
		}
	}
	if h.deps.DB != nil {
		remote, err := audit.HasRemoteActor(ctx, h.deps.DB, audit.ActionSetup, audit.ActionLoginSuccess, audit.ActionRegister, audit.ActionInviteAccept)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		out.ClientIsolationHint = !remote
	}
	writeJSON(w, http.StatusOK, out)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func adminSettingsFrom(v settings.Values, now time.Time) protocol.AdminSettings {
	cidrs := make([]string, 0, len(v.AllowedCIDRs))
	for _, p := range v.AllowedCIDRs {
		cidrs = append(cidrs, p.String())
	}
	out := protocol.AdminSettings{
		InstanceName: v.InstanceName, OpenRegistration: v.OpenRegistration, UploadMaxBytes: v.UploadMaxBytes,
		UserQuotaBytes: v.UserQuotaBytes, DiskReserveBytes: v.DiskReserveBytes, DefaultRetentionDays: v.DefaultRetentionDays,
		WebPushEnabled: v.WebPushEnabled, PushPayload: v.PushPayload, AllowedCIDRs: cidrs,
		SessionIdleDays: v.SessionIdleDays, AlertRenotifyS: v.AlertRenotifyS, MDNSEnabled: v.MDNSEnabled,
		WebResetEnabled: v.WebResetEnabled, RegistrationWindowH: v.RegistrationWindowH, RegistrationOpen: v.RegistrationOpen(now),
	}
	if v.OpenRegistrationEnds != nil {
		t := protocol.FormatTime(v.OpenRegistrationEnds.UnixMilli())
		out.OpenRegistrationUntil = &t
	}
	return out
}

func (h adminAPI) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, adminSettingsFrom(h.auth.Settings().Values(), h.auth.Now()))
}

// patchSettings validates every given key, then stores them all or none.
func (h adminAPI) patchSettings(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if !decodeJSON(w, r, &req) {
		return
	}
	fields := map[string]string{}
	if len(req) == 0 {
		fields["settings"] = "give at least one setting"
	}
	known := map[string]bool{}
	for _, k := range settings.Keys() {
		known[k] = true
	}
	for k, raw := range req {
		if !known[k] {
			fields[k] = "unknown setting"
			continue
		}
		if k == settings.KeyOpenRegistrationEnds {
			fields[k] = "set it with POST /admin/registration"
			continue
		}
		if err := settings.Validate(k, raw); err != nil {
			fields[k] = strings.TrimPrefix(err.Error(), settings.ErrInvalid.Error()+": ")
		}
	}
	if len(fields) > 0 {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid", fields)
		return
	}
	st := h.auth.Settings()
	if err := st.SetManyAt(r.Context(), auditActorFrom(r), h.auth.Now(), req); err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adminSettingsFrom(st.Values(), h.auth.Now()))
}

// registration answers POST /admin/registration: reopen starts a new sign-up
// window, close ends sign-up now.
func (h adminAPI) registration(w http.ResponseWriter, r *http.Request) {
	var req protocol.RegistrationActionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	st := h.auth.Settings()
	var err error
	switch req.Action {
	case protocol.RegistrationReopen:
		err = st.ReopenRegistration(r.Context(), auditActorFrom(r), h.auth.Now())
	case protocol.RegistrationClose:
		err = st.CloseRegistration(r.Context(), auditActorFrom(r))
	default:
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid",
			map[string]string{"action": "must be reopen or close"})
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adminSettingsFrom(st.Values(), h.auth.Now()))
}

// createUser answers POST /admin/users ("Add person").
func (h adminAPI) createUser(w http.ResponseWriter, r *http.Request) {
	var req protocol.AdminCreateUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := h.auth.AdminCreateUser(r.Context(), auditActorFrom(r), auth.AdminCreateUserInput{
		Name: req.Name, Password: req.Password, Role: auth.Role(req.Role),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, adminUser(u))
}

func (h adminAPI) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.auth.ListUsers(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := protocol.AdminUserList{Items: []protocol.User{}}
	for i := range users {
		if users[i].Kind == auth.KindHuman {
			out.Items = append(out.Items, adminUser(&users[i]))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func adminUser(u *auth.User) protocol.User {
	return protocol.User{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Kind: u.Kind, Role: string(u.Role),
		AvatarFileID: u.AvatarFileID, StatusText: u.StatusText, Deactivated: u.DeactivatedAt != nil,
		CreatedAt: protocol.FormatTime(u.CreatedAt),
	}
}

func (h adminAPI) updateUser(w http.ResponseWriter, r *http.Request) {
	var req protocol.UpdateAdminUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	patch := auth.AdminUserPatch{Deactivated: req.Deactivated}
	if req.Role != nil {
		role := auth.Role(*req.Role)
		patch.Role = &role
	}
	u, err := h.auth.AdminUpdateUser(r.Context(), auditActorFrom(r), r.PathValue("id"), patch)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adminUser(u))
}

// passwordReset answers POST /admin/users/{id}/password-reset. The link is
// in this answer only.
func (h adminAPI) passwordReset(w http.ResponseWriter, r *http.Request) {
	code, exp, err := h.auth.CreateReset(r.Context(), auditActorFrom(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, protocol.PasswordResetLink{
		URL: h.baseURL(r) + "/reset/" + code, ExpiresAt: protocol.FormatTime(exp.UnixMilli()),
	})
}

func (h adminAPI) revokeSessions(w http.ResponseWriter, r *http.Request) {
	if err := h.auth.RevokeAllSessions(r.Context(), auditActorFrom(r), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	noContent(w)
}

func adminInvite(inv *auth.Invite) protocol.AdminInvite {
	out := protocol.AdminInvite{
		ID: inv.ID, Role: string(inv.Role), Note: inv.Note, CreatedAt: protocol.FormatTime(inv.CreatedAt),
		ExpiresAt: protocol.FormatTime(inv.ExpiresAt), MaxUses: inv.MaxUses, UseCount: inv.UseCount,
	}
	if inv.CreatedBy != "" {
		out.CreatedBy = &inv.CreatedBy
	}
	if inv.RevokedAt != nil {
		t := protocol.FormatTime(*inv.RevokedAt)
		out.RevokedAt = &t
	}
	return out
}

func (h adminAPI) listInvites(w http.ResponseWriter, r *http.Request) {
	invs, err := h.auth.ListInvites(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := protocol.AdminInviteList{Items: make([]protocol.AdminInvite, 0, len(invs))}
	for i := range invs {
		out.Items = append(out.Items, adminInvite(&invs[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// createInvite answers POST /admin/invites. The URL carries the secret code
// and is in this answer only.
func (h adminAPI) createInvite(w http.ResponseWriter, r *http.Request) {
	var req protocol.CreateInviteRequest
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	if req.ExpiresInSeconds < 0 || req.ExpiresInSeconds > int(auth.MaxInviteExpiry/time.Second) {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid",
			map[string]string{"expires_in": "must be between 1 minute and 90 days"})
		return
	}
	inv, code, err := h.auth.CreateInvite(r.Context(), auditActorFrom(r), auth.CreateInviteInput{
		Role: auth.Role(req.Role), Note: req.Note, ExpiresIn: time.Duration(req.ExpiresInSeconds) * time.Second, MaxUses: req.MaxUses,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, protocol.CreatedInvite{AdminInvite: adminInvite(inv), URL: h.baseURL(r) + "/invite/" + code})
}

func (h adminAPI) revokeInvite(w http.ResponseWriter, r *http.Request) {
	if err := h.auth.RevokeInvite(r.Context(), auditActorFrom(r), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	noContent(w)
}

func (h adminAPI) listChannels(w http.ResponseWriter, r *http.Request) {
	items, err := h.deps.Chat.AdminListChannels(r.Context(), actorFrom(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.AdminChannelList{Items: items})
}

func (h adminAPI) updateChannel(w http.ResponseWriter, r *http.Request) {
	var req protocol.UpdateAdminChannelRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	patch := chat.AdminChannelPatch{IsDefault: req.IsDefault, Archived: req.Archived}
	if req.RetentionDays != nil {
		if string(req.RetentionDays) == "null" {
			patch.RetentionDays = chat.Clear[int]()
		} else {
			var n int
			if err := json.Unmarshal(req.RetentionDays, &n); err != nil {
				WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid",
					map[string]string{"retention_days": "must be a whole number of days or null"})
				return
			}
			patch.RetentionDays = chat.Some(n)
		}
	}
	ch, err := h.deps.Chat.AdminUpdateChannel(r.Context(), auditActorFrom(r), actorFrom(r), r.PathValue("id"), patch)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (h adminAPI) listAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxAuditPage {
			WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid",
				map[string]string{"limit": "must be between 1 and 100"})
			return
		}
		limit = n
	}
	action := q.Get("action")
	if len(action) > 64 {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid", map[string]string{"action": "too long"})
		return
	}
	recs, next, err := audit.List(r.Context(), h.deps.DB, audit.ListParams{Cursor: q.Get("cursor"), Action: action, Limit: limit})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	page := protocol.AuditPage{Items: make([]protocol.AuditEntry, 0, len(recs))}
	for _, rec := range recs {
		e := protocol.AuditEntry{
			ID: rec.ID, At: protocol.FormatTime(rec.At), ActorKind: rec.ActorKind, ActorIP: rec.ActorIP,
			Action: rec.Action, TargetType: rec.TargetType, TargetID: rec.TargetID, Detail: rec.Detail,
		}
		if rec.ActorID != "" {
			id := rec.ActorID
			e.ActorID = &id
		}
		page.Items = append(page.Items, e)
	}
	if next != "" {
		page.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, page)
}

func (h adminAPI) tlsBody() protocol.AdminTLS {
	st := h.deps.Admin.Host().TLS
	return protocol.AdminTLS{
		Mode: string(st.Mode), CAFingerprint: st.CAFingerprint, LeafFingerprint: st.LeafFingerprint,
		LeafNotAfter: protocol.FormatTime(st.LeafNotAfter.UnixMilli()), SANs: nonNil(st.SANs),
	}
}

func (h adminAPI) getTLS(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.tlsBody())
}

func (h adminAPI) reissueTLS(w http.ResponseWriter, r *http.Request) {
	if err := h.deps.Admin.ReissueTLS(); err != nil {
		h.fail(w, r, err)
		return
	}
	actor := auditActorFrom(r)
	err := h.deps.DB.Do(r.Context(), func(tx *store.Tx) error {
		_, err := audit.Write(tx, actor.Entry(audit.ActionTLSReissue, "tls", "", nil))
		return err
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.tlsBody())
}
