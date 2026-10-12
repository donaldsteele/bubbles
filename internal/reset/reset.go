// Package reset implements the two destructive lifecycle operations of a
// bubbles server (pipeline steps 19 and 20): clear content, which deletes
// every message, file, alert and event but keeps accounts, channels, apps and
// settings, and factory reset, which wipes the data directory back to the
// setup wizard.
//
// A reset is an ordered list of idempotent steps journaled in
// <data>/reset-pending.json: the marker is written before the first step,
// rewritten after each one and removed last. Recover, run at server startup
// before any listener opens, completes a reset that a crash or power loss cut
// short. The same steps run inside a live server (internal/app supplies Hooks
// that enter maintenance mode, close sockets and tear down and rebuild the
// services) and from the console with the server stopped (`bubbles reset`).
package reset

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Mode selects what a reset deletes.
type Mode string

// Reset modes.
const (
	ModeContent Mode = protocol.ResetContent
	ModeFactory Mode = protocol.ResetFactory
)

// Audit actions. Details never carry usernames.
const (
	ActionContentCleared = "system.content_cleared"
	ActionFactoryReset   = "system.factory_reset"
	ActionResetDenied    = "system.reset_denied"
)

var (
	// ErrDenied is a reset refused because the typed confirmation or the
	// password was wrong (see AuditDenied).
	ErrDenied = errors.New("reset not confirmed")
	// ErrConfirm is ErrDenied for a confirmation that is not the instance
	// name.
	ErrConfirm = fmt.Errorf("%w: the confirmation does not match the instance name", ErrDenied)
	// ErrPassword is ErrDenied for a wrong password.
	ErrPassword = fmt.Errorf("%w: wrong password", ErrDenied)
	// ErrPending means an unfinished reset is journaled in the data
	// directory; Recover completes it.
	ErrPending = errors.New("an unfinished reset is pending: restart the server to complete it")
	// ErrInProgress means another reset is running right now.
	ErrInProgress = errors.New("a reset is already in progress")
)

// Request is one reset to run.
type Request struct {
	Mode Mode
	// RotateCA destroys the CA and creates a new one (factory only).
	RotateCA bool
	// KeepSettings carries the settings table over (factory only).
	KeepSettings bool
	// Actor is recorded in the audit row (factory keeps its kind and
	// address only: the account it names is gone).
	Actor audit.Actor
}

// Result describes a completed reset.
type Result struct {
	Mode Mode
	// Epoch is the new instance epoch (content).
	Epoch int64
	// SetupToken is the new one-time setup token (factory).
	SetupToken string
	// CARotated reports whether the CA was replaced (factory).
	CARotated bool
}

// Hooks connect the steps to a running server. The zero value runs offline:
// the console with the server stopped, or startup recovery.
type Hooks struct {
	// DB is the live database of a running server. The steps use it until
	// Release and never close it.
	DB *store.DB
	// Quiesce enters maintenance mode, closes every WebSocket with the reset
	// close code and waits for requests in flight.
	Quiesce func(ctx context.Context) error
	// Release stops everything that holds the database and closes it
	// (factory, before any file is deleted: Windows cannot delete open files).
	Release func(ctx context.Context) error
	// ReopenLogs closes the live log file, runs clear and opens a fresh file.
	// Nil: clear runs directly.
	ReopenLogs func(clear func() error) error
	// NewCA creates the new CA and leaf after tls/ was emptied (factory with
	// RotateCA) and starts serving them. Nil: the next server start creates
	// them.
	NewCA func() error
	// Restart runs last, before the marker is removed: it rebuilds the
	// services on the new database (factory) or drops caches (content).
	Restart func(ctx context.Context) error
	// Now and Logger are handed to the database the steps open themselves.
	Now    func() time.Time
	Logger *slog.Logger

	// crash, for tests, is called after every step before (recorded false)
	// and after (recorded true) the marker records it; an error stops the
	// reset there as a crash would.
	crash func(step string, recorded bool) error
}

// Run performs req on dataDir. It refuses with ErrPending while another
// reset's marker is present. On error the marker stays: Recover (at the next
// start) completes the reset.
func Run(ctx context.Context, dataDir string, req Request, h Hooks) (*Result, error) {
	if req.Mode != ModeContent && req.Mode != ModeFactory {
		return nil, fmt.Errorf("reset: unknown mode %q", req.Mode)
	}
	if req.Mode == ModeContent {
		req.RotateCA, req.KeepSettings = false, false
	}
	pending, err := ReadMarker(dataDir)
	if err != nil {
		return nil, err
	}
	if pending != nil {
		return nil, ErrPending
	}
	r := newRunner(dataDir, h)
	defer r.closeDB()
	m := &Marker{
		Version: markerVersion, ID: ulid.Make().String(), Mode: req.Mode,
		RotateCA: req.RotateCA, KeepSettings: req.KeepSettings,
		Actor:     Actor{ID: req.Actor.ID, Kind: req.Actor.Kind, IP: req.Actor.IP},
		StartedAt: r.now().UnixMilli(),
		Done:      []string{},
	}
	if m.Actor.Kind == "" {
		m.Actor.Kind = audit.KindConsole
	}
	if req.Mode == ModeContent || req.KeepSettings {
		db, err := r.database(ctx)
		if err != nil {
			return nil, err
		}
		if req.Mode == ModeContent {
			var epoch int64
			if err := db.Read().QueryRowContext(ctx, `SELECT epoch FROM instance`).Scan(&epoch); err != nil {
				return nil, fmt.Errorf("read instance epoch: %w", err)
			}
			m.Epoch = epoch + 1
		}
		if req.KeepSettings {
			if m.Settings, err = snapshotSettings(ctx, db); err != nil {
				return nil, err
			}
		}
	}
	if err := WriteMarker(dataDir, m); err != nil {
		return nil, err
	}
	r.m = m
	return r.resume(ctx)
}

// RecoverOptions tune Recover; the zero value is ready to use.
type RecoverOptions struct {
	// ReopenLogs is Hooks.ReopenLogs: the caller may already be logging to
	// the log directory a factory reset wipes.
	ReopenLogs func(clear func() error) error
	Now        func() time.Time
	Logger     *slog.Logger
}

// Recover completes the reset journaled in dataDir, if any, with nothing
// else running (call it before opening the database and the listeners). It
// returns nil when there was nothing to do.
func Recover(ctx context.Context, dataDir string, opts RecoverOptions) (*Result, error) {
	m, err := ReadMarker(dataDir)
	if err != nil || m == nil {
		return nil, err
	}
	r := newRunner(dataDir, Hooks{ReopenLogs: opts.ReopenLogs, Now: opts.Now, Logger: opts.Logger})
	defer r.closeDB()
	r.m = m
	return r.resume(ctx)
}

// Preview counts what a reset would delete.
func Preview(ctx context.Context, db *store.DB) (protocol.ResetPreview, error) {
	var p protocol.ResetPreview
	err := db.Read().QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM users WHERE kind = 'human'),
		(SELECT count(*) FROM channels),
		(SELECT count(*) FROM messages),
		(SELECT count(*) FROM files),
		(SELECT COALESCE(SUM(size), 0) FROM (SELECT MAX(size) AS size FROM files GROUP BY sha256))`).
		Scan(&p.Users, &p.Channels, &p.Messages, &p.Files, &p.Bytes)
	if err != nil {
		return protocol.ResetPreview{}, fmt.Errorf("reset preview: %w", err)
	}
	return p, nil
}

// AuditDenied records a refused reset attempt (reason "confirm" or
// "password").
func AuditDenied(ctx context.Context, db *store.DB, actor audit.Actor, mode Mode, reason string) error {
	return db.Do(ctx, func(tx *store.Tx) error {
		_, err := audit.Write(tx, actor.Entry(ActionResetDenied, "instance", "", map[string]string{
			"mode": string(mode), "reason": reason,
		}))
		return err
	})
}

// AwaitingSetup reports whether db was recreated by a factory reset and no
// one has completed setup since: the server then keeps the setup token the
// reset issued instead of replacing it.
func AwaitingSetup(ctx context.Context, db *store.DB) (bool, error) {
	var ok bool
	err := db.Read().QueryRowContext(ctx, `SELECT
		NOT EXISTS (SELECT 1 FROM users) AND EXISTS (SELECT 1 FROM audit_log WHERE action = ?)`, ActionFactoryReset).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("check reset state: %w", err)
	}
	return ok, nil
}

// runner executes the steps of one marker.
type runner struct {
	dataDir string
	hooks   Hooks
	logger  *slog.Logger
	m       *Marker

	db       *store.DB // the database the steps use
	own      bool      // db was opened by the runner, which closes it
	released bool      // Hooks.DB is closed
	token    string    // setup token written by this run
}

func newRunner(dataDir string, h Hooks) *runner {
	logger := h.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &runner{dataDir: dataDir, hooks: h, logger: logger}
}

func (r *runner) now() time.Time {
	if r.hooks.Now != nil {
		return r.hooks.Now()
	}
	return time.Now()
}

// database returns the live database or opens the one in the data
// directory (creating and migrating it when missing).
func (r *runner) database(ctx context.Context) (*store.DB, error) {
	if r.db != nil {
		return r.db, nil
	}
	if r.hooks.DB != nil && !r.released {
		r.db = r.hooks.DB
		return r.db, nil
	}
	db, err := store.Open(ctx, filepath.Join(r.dataDir, store.FileName), store.Options{Now: r.hooks.Now, Logger: r.hooks.Logger})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	r.db, r.own = db, true
	return db, nil
}

// closeDB closes a database the runner opened and forgets the live one.
func (r *runner) closeDB() error {
	db, own := r.db, r.own
	r.db, r.own = nil, false
	if db != nil && own {
		return db.Close()
	}
	return nil
}

// resume runs every step the marker does not list as done, then removes the
// marker.
func (r *runner) resume(ctx context.Context) (*Result, error) {
	for _, st := range r.steps() {
		if r.m.done(st.name) {
			continue
		}
		if err := st.run(ctx, r); err != nil {
			return nil, fmt.Errorf("reset (%s): step %s: %w", r.m.Mode, st.name, err)
		}
		if err := r.crashPoint(st.name, false); err != nil {
			return nil, err
		}
		r.m.Done = append(r.m.Done, st.name)
		if err := WriteMarker(r.dataDir, r.m); err != nil {
			return nil, err
		}
		if err := r.crashPoint(st.name, true); err != nil {
			return nil, err
		}
	}
	res := &Result{Mode: r.m.Mode, Epoch: r.m.Epoch, CARotated: r.m.RotateCA}
	if r.m.Mode == ModeFactory {
		res.SetupToken = r.token
		if res.SetupToken == "" {
			b, err := os.ReadFile(filepath.Join(r.dataDir, auth.SetupTokenFile))
			if err == nil {
				res.SetupToken = strings.TrimSpace(string(b))
			}
		}
	}
	if err := r.closeDB(); err != nil {
		return nil, err
	}
	if err := RemoveMarker(r.dataDir); err != nil {
		return nil, err
	}
	return res, nil
}

func (r *runner) crashPoint(step string, recorded bool) error {
	if r.hooks.crash == nil {
		return nil
	}
	return r.hooks.crash(step, recorded)
}
