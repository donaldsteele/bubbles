package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// registerDeviceRoutes adds the tray device endpoints: device login, the
// handoff code and redemption, and device listing and revocation.
func registerDeviceRoutes(mux *http.ServeMux, d Deps, a *api) {
	public, private := routeKinds(d)
	authn := auth.Middleware(authenticators(d)...)
	// Routes a device token may call itself: authenticated, rate limited per
	// credential, no Origin requirement for the bearer.
	deviceRoute := func(h http.HandlerFunc) http.Handler {
		return chain(h, authn, originCheck, func(next http.Handler) http.Handler {
			return requireUser(d.Auth.APILimiter(), nil, next)
		})
	}

	mux.Handle("POST "+apiPrefix+"/auth/device-login", public(a.deviceLogin))
	mux.Handle("POST "+apiPrefix+"/auth/device-register", public(a.deviceRegister))
	mux.Handle("POST "+apiPrefix+"/auth/handoff-code", deviceRoute(a.handoffCode))
	mux.Handle("GET /auth/handoff", http.HandlerFunc(a.handoff))
	mux.Handle("GET "+apiPrefix+"/me/devices", private(a.listDevices))
	mux.Handle("DELETE "+apiPrefix+"/me/devices/{id}", deviceRoute(a.revokeDevice))
}

func (a *api) deviceLogin(w http.ResponseWriter, r *http.Request) {
	var req protocol.DeviceLoginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	fields := map[string]string{}
	if req.Username == "" {
		fields["username"] = "required"
	}
	if req.Password == "" {
		fields["password"] = "required"
	}
	if strings.TrimSpace(req.DeviceName) == "" {
		fields["device_name"] = "required"
	}
	if len(fields) > 0 {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid", fields)
		return
	}
	res, err := a.auth.DeviceLogin(r.Context(), auth.DeviceLoginInput{
		Username: req.Username, Password: req.Password, DeviceName: req.DeviceName,
		UserAgent: r.UserAgent(), IP: clientIP(r),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.DeviceLoginResponse{
		DeviceToken: res.Token, DeviceID: res.DeviceID, HandoffCode: res.HandoffCode,
	})
}

func (a *api) deviceRegister(w http.ResponseWriter, r *http.Request) {
	var req protocol.DeviceRegisterRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	fields := map[string]string{}
	if strings.TrimSpace(req.Name) == "" {
		fields["name"] = "required"
	}
	if req.Password == "" {
		fields["password"] = "required"
	}
	if strings.TrimSpace(req.DeviceName) == "" {
		fields["device_name"] = "required"
	}
	if len(fields) > 0 {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid", fields)
		return
	}
	res, err := a.auth.DeviceRegister(r.Context(), auth.DeviceRegisterInput{
		Name: req.Name, Password: req.Password, DeviceName: req.DeviceName,
		UserAgent: r.UserAgent(), IP: clientIP(r),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, protocol.DeviceLoginResponse{
		DeviceToken: res.Token, DeviceID: res.DeviceID, HandoffCode: res.HandoffCode,
	})
}

func (a *api) handoffCode(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r.Context())
	if p.Via == auth.ViaBearer {
		WriteError(w, http.StatusForbidden, CodeForbidden, "this endpoint is not available to app tokens")
		return
	}
	if p.Device == nil {
		WriteError(w, http.StatusForbidden, CodeForbidden, "a device token is required")
		return
	}
	code, err := a.auth.IssueHandoff(p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.HandoffCodeResponse{HandoffCode: code})
}

// handoff redeems a one-time code: it starts a web session (cookie) and
// redirects to the app, or to the optional same-origin path next. Any problem
// redirects to the login page.
func (a *api) handoff(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	replace := ""
	if ck, err := r.Cookie(auth.CookieName); err == nil {
		replace = ck.Value
	}
	res, err := a.auth.RedeemHandoff(r.Context(), r.URL.Query().Get("code"), r.UserAgent(), clientIP(r), replace)
	if err != nil {
		if !isNotFound(err) {
			a.logger.Warn("handoff failed", "request_id", RequestID(r.Context()), "err", err)
		}
		http.Redirect(w, r, "/login?error=handoff", http.StatusSeeOther)
		return
	}
	http.SetCookie(w, auth.NewCookie(res.Token, res.Session.ExpiresAt(), timeFromMillis(res.Session.CreatedAt)))
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
}

// safeNext returns next when it is a plain same-origin absolute path (the
// rule of the web app's safeNext), else "/": no scheme-relative or backslash
// forms, no control characters, nothing url.Parse reads as having a scheme
// or host.
func safeNext(next string) string {
	if next == "" || len(next) > 2048 || next[0] != '/' || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	for _, r := range next {
		if r < 0x20 || r == 0x7f {
			return "/"
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") {
		return "/"
	}
	return next
}

func (a *api) listDevices(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r.Context())
	list, err := a.auth.ListDevices(r.Context(), p.User.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := protocol.DeviceList{Items: make([]protocol.Device, 0, len(list))}
	for _, dv := range list {
		item := protocol.Device{
			ID: dv.ID, Name: dv.Name, Prefix: dv.Prefix, Scopes: dv.Scopes,
			CreatedAt: protocol.FormatTime(dv.CreatedAt),
		}
		if dv.LastUsedAt > 0 {
			s := protocol.FormatTime(dv.LastUsedAt)
			item.LastUsedAt = &s
		}
		out.Items = append(out.Items, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// revokeDevice revokes a device. A browser session may revoke any of its
// user's devices; a device token only itself (the tray's "sign out").
func (a *api) revokeDevice(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFrom(r.Context())
	if p.Via == auth.ViaBearer {
		WriteError(w, http.StatusForbidden, CodeForbidden, "this endpoint is not available to app tokens")
		return
	}
	id := r.PathValue("id")
	if p.Device != nil && p.Device.ID != id {
		WriteError(w, http.StatusForbidden, CodeForbidden, "a device token can only revoke itself")
		return
	}
	if err := a.auth.RevokeDevice(r.Context(), audit.User(p.User.ID, clientIP(r)), p.User.ID, id); err != nil {
		a.fail(w, r, err)
		return
	}
	noContent(w)
}

func isNotFound(err error) bool { return errors.Is(err, auth.ErrNotFound) }

func timeFromMillis(ms int64) time.Time { return time.UnixMilli(ms) }
