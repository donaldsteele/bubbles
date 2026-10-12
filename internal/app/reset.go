package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/backup"
	"github.com/donaldsteele/bubbles/internal/buildinfo"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/httpapi"
	"github.com/donaldsteele/bubbles/internal/reset"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/internal/tlsca"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// DrainTimeout bounds how long a reset waits for requests in flight (after
// closing the gate and the WebSockets) before it deletes anything anyway.
const DrainTimeout = 3 * time.Second

// Reset runs req on the live server (pipeline steps 19 and 20): the steps of
// internal/reset with hooks that enter maintenance mode, close every
// WebSocket with 4011 (content) or 4010 (factory), wait for requests in
// flight, and for a factory reset tear the services down, swap the CA when
// asked and rebuild everything on the new database. Confirmation and
// password checks are the caller's. If a step fails after maintenance mode
// began, the server stays in maintenance: a restart completes the reset.
func (a *App) Reset(ctx context.Context, req reset.Request) (*reset.Result, error) {
	if !a.resetMu.TryLock() {
		return nil, reset.ErrInProgress
	}
	defer a.resetMu.Unlock()
	// Once started a reset runs to the end even if its client goes away.
	ctx = context.WithoutCancel(ctx)
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	if a.closed {
		return nil, store.ErrClosed
	}
	if req.Mode == reset.ModeFactory && req.RotateCA && a.Certs().Mode() == tlsca.ModeBYO {
		return nil, tlsca.ErrBYO
	}
	self := a.gate.self(ctx)
	quiesced := false
	code, reason := events.CloseContentCleared, events.ReasonContentCleared
	if req.Mode == reset.ModeFactory {
		code, reason = events.CloseServerReset, events.ReasonServerReset
	}
	h := reset.Hooks{
		DB: a.db, Now: a.opts.Now, Logger: a.logger, ReopenLogs: a.reopenLogs(),
		Quiesce: func(context.Context) error {
			quiesced = true
			a.gate.enter()
			a.ws.CloseAll(code, reason)
			if !a.gate.drain(self, DrainTimeout) {
				a.logger.Warn("reset: requests still in flight after the drain timeout", "timeout", DrainTimeout.String())
			}
			return nil
		},
	}
	if req.Mode == reset.ModeFactory {
		h.Release = a.release
		h.NewCA = a.newCA
		h.Restart = a.rebuild
	} else {
		h.Restart = func(context.Context) error {
			a.hub.Membership().InvalidateAll()
			a.sweepTrash()
			return nil
		}
	}
	res, err := reset.Run(ctx, a.cfg.DataDir, req, h)
	if err != nil {
		if quiesced {
			a.reportIncompleteReset(req.Mode, err)
		}
		return nil, err
	}
	a.gate.leave()
	a.logger.Info("reset complete", "mode", string(req.Mode))
	if req.Mode == reset.ModeFactory {
		// rebuild bootstrapped the new admin; hand its password to the caller
		// and the console, then forget it.
		res.AdminPassword = a.adminPassword
		a.printFactoryReset(res)
	}
	return res, nil
}

// reportIncompleteReset tells the operator, in the log and on the console,
// that a reset step failed and the server is stuck in maintenance mode.
func (a *App) reportIncompleteReset(mode reset.Mode, err error) {
	a.logger.Error("reset incomplete - restart bubbles to resume; the server stays in maintenance mode until then",
		"mode", string(mode), "err", err)
	fmt.Fprintf(a.opts.Stderr, "\n  ERROR: the %s reset did not finish (%v).\n  Reset incomplete - restart bubbles to resume it.\n\n", mode, err)
}

// reopenLogs adapts Options.LogFiles to reset.Hooks.ReopenLogs.
func (a *App) reopenLogs() func(clear func() error) error {
	if a.opts.LogFiles == nil {
		return nil
	}
	return a.opts.LogFiles.Reopen
}

// release stops everything that holds the database and closes it (factory
// reset, before any file is deleted).
func (a *App) release(context.Context) error {
	if a.running {
		a.stopBackground()
	}
	a.notify.Close()
	a.push.Close()
	a.hub.Close()
	if err := a.db.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	return nil
}

// newCA creates the new CA and leaf in the emptied tls directory and serves
// them from the next TLS handshake on.
func (a *App) newCA() error {
	m, err := CertManager(a.cfg, a.logger, a.opts)
	if err != nil {
		return err
	}
	a.certs.Store(m)
	a.caRotated = true
	a.logger.Info("factory reset replaced the CA", "fingerprint", m.CA().Fingerprint())
	return nil
}

// rebuild reopens the recreated database, rebuilds every service and both
// handlers, restarts the background work and re-announces mDNS when the CA
// changed.
func (a *App) rebuild(ctx context.Context) error {
	a.isolationDone.Store(false)
	if err := a.openCore(ctx); err != nil {
		return err
	}
	if a.running {
		a.startBackground()
	}
	if a.caRotated {
		a.caRotated = false
		a.RefreshMDNS()
	}
	return nil
}

// caFingerprint is what devices pin: the CA's, or the served certificate's
// with --tls-cert.
func (a *App) caFingerprint() string {
	st := a.Certs().Status()
	if st.Mode == tlsca.ModeBYO {
		return st.LeafFingerprint
	}
	return st.CAFingerprint
}

// printFactoryReset tells the console the server was reset and how to sign
// in to the new admin account (the password never goes to the log).
func (a *App) printFactoryReset(res *reset.Result) {
	w := a.opts.Stderr
	fmt.Fprintf(w, "\nbubbles was factory reset; sign in with the new admin account\n\n")
	fmt.Fprintf(w, "  %-16s %s\n", "Open:", a.URLs()[0])
	fmt.Fprintf(w, "  %-16s %s\n", "Sign in as:", auth.BootstrapAdminUsername)
	fmt.Fprintf(w, "  %-16s %s\n", "Password:", res.AdminPassword)
	note := ""
	if res.CARotated {
		note = " (new CA: every device must trust it again)"
	}
	fmt.Fprintf(w, "  %-16s %s%s\n\n", "CA fingerprint:", a.caFingerprint(), note)
	a.adminPassword = ""
}

// resetAPI is the httpapi.Resetter of one generation of services: it checks
// confirmations against the settings and passwords against the accounts it
// was built with, and runs the reset on the App.
type resetAPI struct {
	app      *App
	db       *store.DB
	settings *settings.Store
	auth     *auth.Service
}

func (r *resetAPI) ResetPreview(ctx context.Context) (protocol.ResetPreview, error) {
	return reset.Preview(ctx, r.db)
}

func (r *resetAPI) ClearContent(ctx context.Context, in httpapi.ResetInput) (protocol.ContentCleared, error) {
	if err := r.authorize(ctx, reset.ModeContent, in); err != nil {
		return protocol.ContentCleared{}, err
	}
	res, err := r.app.Reset(ctx, reset.Request{Mode: reset.ModeContent, Actor: in.Actor})
	if err != nil {
		return protocol.ContentCleared{}, err
	}
	return protocol.ContentCleared{Epoch: res.Epoch}, nil
}

func (r *resetAPI) FactoryReset(ctx context.Context, in httpapi.ResetInput) (protocol.FactoryReset, error) {
	if err := r.authorize(ctx, reset.ModeFactory, in); err != nil {
		return protocol.FactoryReset{}, err
	}
	res, err := r.app.Reset(ctx, reset.Request{
		Mode: reset.ModeFactory, RotateCA: in.RotateCA, KeepSettings: in.KeepSettings, Actor: in.Actor,
	})
	if err != nil {
		return protocol.FactoryReset{}, err
	}
	return protocol.FactoryReset{
		AdminUsername: auth.BootstrapAdminUsername, AdminPassword: res.AdminPassword, SignInURL: r.app.URLs()[0],
		CARotated: res.CARotated, CAFingerprint: r.app.caFingerprint(),
	}, nil
}

func (r *resetAPI) Backup(ctx context.Context, w io.Writer, passphrase string) error {
	return backup.Create(ctx, r.app.cfg.DataDir, w, backup.CreateOptions{
		Passphrase: passphrase, WorkFactor: r.app.opts.BackupWorkFactor,
		Now: r.app.opts.Now, Version: buildinfo.Version,
	})
}

// authorize checks the typed instance name, then the password, and audits
// a refusal (reason "confirm" or "password").
func (r *resetAPI) authorize(ctx context.Context, mode reset.Mode, in httpapi.ResetInput) error {
	var denied error
	switch {
	case strings.TrimSpace(in.Confirm) != r.settings.InstanceName():
		denied = reset.ErrConfirm
	default:
		err := r.auth.VerifyPassword(ctx, in.Actor.ID, in.Password)
		if errors.Is(err, auth.ErrWrongPassword) {
			denied = reset.ErrPassword
		} else if err != nil {
			return err
		}
	}
	if denied == nil {
		return nil
	}
	reason := "confirm"
	if denied == reset.ErrPassword {
		reason = "password"
	}
	if err := reset.AuditDenied(ctx, r.db, in.Actor, mode, reason); err != nil {
		r.app.logger.Warn("audit refused reset", "err", err)
	}
	return denied
}
