package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/files"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// api holds what the REST handlers share.
type api struct {
	auth   *auth.Service
	chat   *chat.Service
	files  *files.Service
	logger *slog.Logger
	// posts limits message posts per user (10/s, burst 20) on top of the
	// per-session limit every private route has.
	posts *auth.BucketLimiter
}

// apiPrefix is the REST API base path.
const apiPrefix = "/api/v1"

// registerAuthRoutes adds the setup, auth, invite, register, password-reset
// and /me endpoints. Every route gets, in order: authentication (cookie ->
// principal, never rejecting), the Origin check, and for routes that need a
// signed-in user the 401 gate plus the per-session rate limit.
func registerAuthRoutes(mux *http.ServeMux, d Deps) {
	a := &api{auth: d.Auth, logger: d.Logger}
	public, private := routeKinds(d)

	mux.Handle("GET "+apiPrefix+"/setup/status", public(a.setupStatus))
	mux.Handle("POST "+apiPrefix+"/setup", public(a.setup))
	mux.Handle("POST "+apiPrefix+"/auth/login", public(a.login))
	mux.Handle("POST "+apiPrefix+"/auth/logout", public(a.logout))
	mux.Handle("GET "+apiPrefix+"/invites/{code}", public(a.inviteInfo))
	mux.Handle("POST "+apiPrefix+"/invites/{code}/accept", public(a.acceptInvite))
	mux.Handle("POST "+apiPrefix+"/register", public(a.register))
	mux.Handle("POST "+apiPrefix+"/password-resets/{code}", public(a.resetPassword))

	mux.Handle("GET "+apiPrefix+"/me", private(a.me))
	mux.Handle("PATCH "+apiPrefix+"/me", private(a.patchMe))
	mux.Handle("POST "+apiPrefix+"/me/password", private(a.changePassword))
	mux.Handle("GET "+apiPrefix+"/me/sessions", private(a.listSessions))
	mux.Handle("DELETE "+apiPrefix+"/me/sessions/{id}", private(a.revokeSession))
	registerDeviceRoutes(mux, d, a)
}

// authenticators returns the credentials the server accepts, in order: the
// session cookie, a tray device token and, when the apps service is wired, an app bearer token.
func authenticators(d Deps) []auth.Authenticator {
	auths := []auth.Authenticator{d.Auth.CookieAuthenticator(), d.Auth.DeviceAuthenticator()}
	if d.Apps != nil {
		auths = append(auths, d.Apps.Authenticator())
	}
	return auths
}

// tokenLimiter is the per-token request limit, nil without the apps service.
func tokenLimiter(d Deps) *auth.BucketLimiter {
	if d.Apps == nil {
		return nil
	}
	return d.Apps.Limiter()
}

// routeKinds returns the two wrappers of routes for signed-in humans: public
// routes get authentication (cookie or bearer -> principal, never rejecting)
// and the Origin check; private routes add the 401 gate and the per-session
// rate limit. Both answer 403 to a valid app token: these are the cookie-only
// endpoints (setup, auth, invites, register, password resets, /me, push and
// the admin API). Routes an app may call use appRoute instead.
func routeKinds(d Deps) (public, private func(http.HandlerFunc) http.Handler) {
	authn := auth.Middleware(authenticators(d)...)
	public = func(h http.HandlerFunc) http.Handler {
		return chain(h, authn, humansOnly, originCheck)
	}
	private = func(h http.HandlerFunc) http.Handler {
		return chain(h, authn, humansOnly, originCheck, func(next http.Handler) http.Handler {
			return requireUser(d.Auth.APILimiter(), nil, next)
		})
	}
	return public, private
}

// appRoute wraps a route that signed-in humans and app tokens may both call.
// A cookie principal is limited per session, a bearer principal per token
// (10 requests per second, burst 30). The Origin check is skipped for bearer
// requests: they are not sent by a browser with ambient credentials.
func appRoute(d Deps) func(http.HandlerFunc) http.Handler {
	authn := auth.Middleware(authenticators(d)...)
	return func(h http.HandlerFunc) http.Handler {
		return chain(h, authn, deviceGate, originCheck, func(next http.Handler) http.Handler {
			return requireUser(d.Auth.APILimiter(), tokenLimiter(d), next)
		})
	}
}

// fail maps a service error to the REST error model: the one place auth and
// chat errors become HTTP answers. An invisible resource is always 404, never
// 403; anything unrecognised is a logged 500 that leaks no internals.
func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	var (
		ve  *auth.ValidationError
		cve *chat.ValidationError
		rl  *auth.RateLimitedError
		big *files.TooLargeError
	)
	switch {
	case errors.As(err, &cve):
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid", cve.Fields)
	case errors.Is(err, chat.ErrNotFound):
		WriteError(w, http.StatusNotFound, CodeNotFound, "not found")
	case errors.Is(err, chat.ErrForbidden):
		WriteError(w, http.StatusForbidden, CodeForbidden, chatMessage(err, "you are not allowed to do that"))
	case errors.Is(err, chat.ErrConflict):
		WriteError(w, http.StatusConflict, CodeConflict, chatMessage(err, "the request conflicts with the current state"))
	case errors.As(err, &ve):
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid", ve.Fields)
	case errors.As(err, &rl):
		WriteRateLimited(w, rl.RetryAfter)
	case errors.Is(err, auth.ErrInvalidCredentials):
		// One message for every login failure: no user enumeration.
		WriteError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid username or password")
	case errors.Is(err, auth.ErrWrongPassword):
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "the current password is incorrect", map[string]string{"current": "incorrect"})
	case errors.Is(err, auth.ErrSetupToken):
		WriteError(w, http.StatusForbidden, CodeForbidden, "invalid setup token")
	case errors.Is(err, auth.ErrSetupDone):
		WriteError(w, http.StatusConflict, CodeConflict, "setup has already been completed")
	case errors.Is(err, auth.ErrUsernameTaken):
		WriteFieldErrors(w, http.StatusConflict, CodeConflict, "that username is already taken", map[string]string{"username": "already taken"})
	case errors.Is(err, auth.ErrInviteInvalid), errors.Is(err, auth.ErrResetInvalid), errors.Is(err, auth.ErrNotFound):
		WriteError(w, http.StatusNotFound, CodeNotFound, notFoundMessage(err))
	case errors.Is(err, auth.ErrRegistrationClosed):
		WriteError(w, http.StatusForbidden, CodeForbidden, "registration is closed")
	case errors.As(err, &big):
		WriteErrorDetails(w, http.StatusRequestEntityTooLarge, CodeTooLarge, tooLargeMessage(big), big.Details())
	case errors.Is(err, files.ErrNotFound):
		WriteError(w, http.StatusNotFound, CodeNotFound, "not found")
	case errors.Is(err, files.ErrInsufficientStorage):
		WriteError(w, http.StatusInsufficientStorage, CodeInsufficientStorage, "the server is out of disk space")
	case errors.Is(err, files.ErrTooManyUploads):
		WriteRateLimited(w, time.Second)
	case errors.Is(err, files.ErrBadUpload):
		WriteError(w, http.StatusBadRequest, CodeBadRequest, "the upload could not be read")
	case errors.Is(err, store.ErrClosed), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		WriteError(w, http.StatusServiceUnavailable, CodeUnavailable, "the server is busy or shutting down; try again")
	default:
		a.logger.Error("request failed", "request_id", RequestID(r.Context()), "method", r.Method, "err", err)
		WriteError(w, http.StatusInternalServerError, CodeInternal, "internal server error")
	}
}

// chatMessage is the client-safe text of a typed chat error, else fallback.
func chatMessage(err error, fallback string) string {
	var ce *chat.Error
	if errors.As(err, &ce) && ce.Msg != "" {
		return ce.Msg
	}
	return fallback
}

func notFoundMessage(err error) string {
	switch {
	case errors.Is(err, auth.ErrInviteInvalid):
		return "this invite is invalid or has expired"
	case errors.Is(err, auth.ErrResetInvalid):
		return "this password reset link is invalid or has expired"
	}
	return "not found"
}

func userToProtocol(u *auth.User) protocol.User {
	return protocol.User{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Kind: u.Kind, Role: string(u.Role),
		AvatarFileID: u.AvatarFileID, StatusText: u.StatusText, Deactivated: u.DeactivatedAt != nil,
		CreatedAt: protocol.FormatTime(u.CreatedAt),
	}
}

func meToProtocol(u *auth.User) protocol.Me {
	prefs := u.Prefs
	if len(prefs) == 0 {
		prefs = json.RawMessage("{}")
	}
	return protocol.Me{User: userToProtocol(u), Prefs: prefs, MustChange: u.MustChangePW}
}

// startSession sets the session cookie and answers with the new user.
func startSession(w http.ResponseWriter, res *auth.SessionResult, status int) {
	http.SetCookie(w, auth.NewCookie(res.Token, res.Session.ExpiresAt(), time.UnixMilli(res.Session.CreatedAt)))
	writeJSON(w, status, protocol.AuthResponse{User: meToProtocol(res.User)})
}

func noContent(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) setupStatus(w http.ResponseWriter, r *http.Request) {
	needs, err := a.auth.NeedsSetup(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.SetupStatus{NeedsSetup: needs})
}

func (a *api) setup(w http.ResponseWriter, r *http.Request) {
	var req protocol.SetupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := a.auth.Setup(r.Context(), auth.SetupInput{
		Token: req.SetupToken, Username: req.Username, Password: req.Password, DisplayName: req.DisplayName,
		InstanceName: req.InstanceName, UserAgent: r.UserAgent(), IP: clientIP(r),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	startSession(w, res, http.StatusCreated)
}

func (a *api) login(w http.ResponseWriter, r *http.Request) {
	var req protocol.LoginRequest
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
	if len(fields) > 0 {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "username and password are required", fields)
		return
	}
	in := auth.LoginInput{Username: req.Username, Password: req.Password, UserAgent: r.UserAgent(), IP: clientIP(r)}
	if ck, err := r.Cookie(auth.CookieName); err == nil {
		in.ReplaceToken = ck.Value
	}
	res, err := a.auth.Login(r.Context(), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	startSession(w, res, http.StatusOK)
}

func (a *api) logout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(auth.CookieName); err == nil {
		if err := a.auth.Logout(r.Context(), ck.Value, clientIP(r)); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	http.SetCookie(w, auth.ClearCookie())
	noContent(w)
}

func (a *api) inviteInfo(w http.ResponseWriter, r *http.Request) {
	st, err := a.auth.LookupInvite(r.Context(), r.PathValue("code"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	info := protocol.InviteInfo{Valid: st.Valid, InstanceName: a.auth.Settings().InstanceName()}
	if st.Valid {
		info.Role = string(st.Role)
	}
	writeJSON(w, http.StatusOK, info)
}

func (a *api) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var req protocol.AcceptInviteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := a.auth.AcceptInvite(r.Context(), auth.AcceptInviteInput{
		Code: r.PathValue("code"), Username: req.Username, Password: req.Password, DisplayName: req.DisplayName,
		UserAgent: r.UserAgent(), IP: clientIP(r),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	startSession(w, res, http.StatusCreated)
}

func (a *api) register(w http.ResponseWriter, r *http.Request) {
	var req protocol.RegisterRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := a.auth.Register(r.Context(), auth.RegisterInput{
		Username: req.Username, Password: req.Password, DisplayName: req.DisplayName,
		UserAgent: r.UserAgent(), IP: clientIP(r),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	startSession(w, res, http.StatusCreated)
}

func (a *api) resetPassword(w http.ResponseWriter, r *http.Request) {
	var req protocol.ResetPasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := a.auth.ResetPassword(r.Context(), r.PathValue("code"), req.Password, clientIP(r)); err != nil {
		a.fail(w, r, err)
		return
	}
	noContent(w)
}
