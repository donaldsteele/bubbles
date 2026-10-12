package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/reset"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/internal/tlsca"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// ResetAttemptsPerHour is how many reset attempts (right or wrong) one admin
// gets per hour.
const ResetAttemptsPerHour = 3

// ResetInput is a confirmed-looking reset request; the Resetter checks
// Confirm against the instance name and Password against the actor's account
// and audits a refusal.
type ResetInput struct {
	Actor        audit.Actor
	Confirm      string
	Password     string
	RotateCA     bool
	KeepSettings bool
}

// Resetter runs the reset lifecycle of the server (the composition root
// implements it). A wrong confirmation or password is an error matching
// reset.ErrConfirm or reset.ErrPassword.
type Resetter interface {
	ResetPreview(ctx context.Context) (protocol.ResetPreview, error)
	ClearContent(ctx context.Context, in ResetInput) (protocol.ContentCleared, error)
	FactoryReset(ctx context.Context, in ResetInput) (protocol.FactoryReset, error)
	// Backup streams an encrypted backup archive of the data directory.
	Backup(ctx context.Context, w io.Writer, passphrase string) error
}

// registerResetRoutes adds the Danger-zone endpoints: admin role and a
// browser session only (app and device tokens are refused), Origin-checked.
func registerResetRoutes(mux *http.ServeMux, d Deps) {
	h := lifecycleAPI{
		api:      &api{auth: d.Auth, logger: d.Logger},
		reset:    d.Reset,
		now:      d.Now,
		db:       d.DB,
		attempts: auth.NewBucketLimiter(d.Now, ResetAttemptsPerHour/3600.0, ResetAttemptsPerHour),
	}
	admin := adminRoute(d)
	mux.Handle("GET "+apiPrefix+"/admin/reset/preview", admin(h.preview))
	mux.Handle("POST "+apiPrefix+"/admin/reset", admin(h.resetServer))
	mux.Handle("POST "+apiPrefix+"/admin/backup", admin(h.backup))
}

type lifecycleAPI struct {
	*api
	reset    Resetter
	now      func() time.Time
	db       *store.DB
	attempts *auth.BucketLimiter
}

func (h lifecycleAPI) preview(w http.ResponseWriter, r *http.Request) {
	p, err := h.reset.ResetPreview(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// resetServer answers POST /admin/reset. Every attempt counts against the
// admin's hourly allowance before anything is checked.
func (h lifecycleAPI) resetServer(w http.ResponseWriter, r *http.Request) {
	if !h.auth.Settings().Values().WebResetEnabled {
		h.auditDisabled(r)
		WriteError(w, http.StatusForbidden, CodeForbidden, "reset from the web is disabled; run `bubbles reset` on the server console")
		return
	}
	if ok, wait := h.attempts.Allow(auth.UserFrom(r.Context()).ID); !ok {
		WriteRateLimited(w, wait)
		return
	}
	var req protocol.ResetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	fields := map[string]string{}
	switch req.Mode {
	case protocol.ResetFactory:
	case protocol.ResetContent:
		if req.RotateCA {
			fields["rotate_ca"] = "only for a factory reset"
		}
		if req.KeepSettings {
			fields["keep_settings"] = "only for a factory reset"
		}
	default:
		fields["mode"] = "must be content or factory"
	}
	if req.Confirm == "" {
		fields["confirm"] = "type the instance name to confirm"
	}
	if req.Password == "" {
		fields["password"] = "required"
	}
	if len(fields) > 0 {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid", fields)
		return
	}
	in := ResetInput{
		Actor: auditActorFrom(r), Confirm: req.Confirm, Password: req.Password,
		RotateCA: req.RotateCA, KeepSettings: req.KeepSettings,
	}
	if req.Mode == protocol.ResetContent {
		res, err := h.reset.ClearContent(r.Context(), in)
		if err != nil {
			h.resetFail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	res, err := h.reset.FactoryReset(r.Context(), in)
	if err != nil {
		h.resetFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// auditDisabled records an attempt made while web reset is switched off, so
// the console operator can see someone tried. The mode is read from the body
// when it parses; the audit failing must not change the 403.
func (h lifecycleAPI) auditDisabled(r *http.Request) {
	if h.db == nil {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	mode := reset.Mode(req.Mode)
	if mode != reset.ModeContent && mode != reset.ModeFactory {
		mode = ""
	}
	if err := reset.AuditDenied(r.Context(), h.db, auditActorFrom(r), mode, "disabled"); err != nil {
		h.logger.Warn("audit disabled reset attempt", "err", err)
	}
}

func (h lifecycleAPI) resetFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, reset.ErrConfirm):
		WriteError(w, http.StatusForbidden, CodeForbidden, "the confirmation does not match the instance name")
	case errors.Is(err, reset.ErrPassword):
		WriteError(w, http.StatusForbidden, CodeForbidden, "the password is incorrect")
	case errors.Is(err, reset.ErrInProgress), errors.Is(err, reset.ErrPending):
		WriteError(w, http.StatusConflict, CodeConflict, err.Error())
	case errors.Is(err, tlsca.ErrBYO):
		WriteError(w, http.StatusConflict, CodeConflict, "the server uses --tls-cert/--tls-key, so it has no CA to replace")
	default:
		h.fail(w, r, err)
	}
}

// backup answers POST /admin/backup with the age-encrypted archive as a
// download. Headers go out with the first archive byte, so a failure before
// it is an ordinary JSON error; a failure mid-stream aborts the connection
// so the client never mistakes a truncated archive for a complete one.
func (h lifecycleAPI) backup(w http.ResponseWriter, r *http.Request) {
	var req protocol.BackupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Passphrase == "" {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid",
			map[string]string{"passphrase": "required: backups are always encrypted"})
		return
	}
	name := "bubbles-" + h.now().UTC().Format("20060102-150405") + ".bbk"
	out := &lazyDownload{w: w, name: name}
	if err := h.reset.Backup(r.Context(), out, req.Passphrase); err != nil {
		if !out.started {
			h.fail(w, r, err)
			return
		}
		h.logger.Error("backup download failed", "request_id", RequestID(r.Context()), "err", err)
		panic(http.ErrAbortHandler)
	}
	out.start()
}

// lazyDownload sends the download headers just before the first byte.
type lazyDownload struct {
	w       http.ResponseWriter
	name    string
	started bool
}

func (l *lazyDownload) start() {
	if l.started {
		return
	}
	l.started = true
	h := l.w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", `attachment; filename="`+l.name+`"`)
	h.Set("Cache-Control", "no-store")
	l.w.WriteHeader(http.StatusOK)
}

func (l *lazyDownload) Write(p []byte) (int, error) {
	l.start()
	return l.w.Write(p)
}
