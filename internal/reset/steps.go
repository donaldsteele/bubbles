package reset

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/push"
	"github.com/donaldsteele/bubbles/internal/store"
)

// Step names, as journaled in the marker.
const (
	StepAudit           = "audit"
	StepQuiesce         = "quiesce"
	StepClearContent    = "clear-content"
	StepVacuum          = "vacuum"
	StepTrashFiles      = "trash-files"
	StepWipeTmp         = "wipe-tmp"
	StepDeleteSnapshots = "delete-snapshots"
	StepRelease         = "release"
	StepDeleteDB        = "delete-db"
	StepDeleteSecrets   = "delete-secrets"
	StepWipeLogs        = "wipe-logs"
	StepWipeBackups     = "wipe-backups"
	StepRotateCA        = "rotate-ca"
	StepOpenDB          = "open-db"
	StepRestoreSettings = "restore-settings"
	StepVAPID           = "vapid"
	StepSetupToken      = "setup-token"
	StepRestart         = "restart"
)

// step is one idempotent unit of a reset: running it again after it
// completed (a crash before the marker recorded it) is harmless.
type step struct {
	name string
	run  func(ctx context.Context, r *runner) error
}

// steps returns the ordered step list of the marker's mode and options.
func (r *runner) steps() []step {
	if r.m.Mode == ModeContent {
		// Pipeline 19.
		return []step{
			{StepAudit, auditContent},
			{StepQuiesce, quiesce},
			{StepClearContent, clearContent},
			{StepVacuum, vacuum},
			{StepTrashFiles, trashFiles},
			{StepWipeTmp, wipeTmp},
			{StepDeleteSnapshots, deleteSnapshots},
			{StepRestart, restart},
		}
	}
	// Pipeline 20.
	out := []step{
		{StepQuiesce, quiesce},
		{StepRelease, release},
		{StepDeleteDB, deleteDB},
		{StepDeleteSecrets, deleteSecrets},
		{StepWipeLogs, wipeLogs},
		{StepWipeBackups, wipeBackups},
		{StepWipeTmp, wipeTmp},
		{StepTrashFiles, trashFiles},
	}
	if r.m.RotateCA {
		out = append(out, step{StepRotateCA, rotateCA})
	}
	out = append(out, step{StepOpenDB, openDB})
	if r.m.KeepSettings {
		out = append(out, step{StepRestoreSettings, restoreSettingsStep})
	}
	return append(out,
		step{StepVAPID, newVAPID},
		step{StepSetupToken, newSetupToken},
		step{StepAudit, auditFactory},
		step{StepRestart, restart},
	)
}

func (r *runner) actor() audit.Actor {
	a := audit.Actor{ID: r.m.Actor.ID, Kind: r.m.Actor.Kind, IP: r.m.Actor.IP}
	if a.Kind == "" {
		a.Kind = audit.KindConsole
	}
	return a
}

// auditContent records system.content_cleared once per reset (the reset id
// in the detail makes a re-run a no-op).
func auditContent(ctx context.Context, r *runner) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	return db.Do(ctx, func(tx *store.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM audit_log WHERE action = ? AND json_extract(detail, '$.reset_id') = ?`,
			ActionContentCleared, r.m.ID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		_, err := audit.Write(tx, r.actor().Entry(ActionContentCleared, "instance", "", map[string]string{"reset_id": r.m.ID}))
		return err
	})
}

func quiesce(ctx context.Context, r *runner) error {
	if r.hooks.Quiesce == nil {
		return nil
	}
	return r.hooks.Quiesce(ctx)
}

// contentStatements empty every content table in one transaction. Messages
// go after the rows that reference them; their FTS delete triggers keep the
// index consistent, and 'delete-all' + 'optimize' then rebuild it empty.
var contentStatements = []string{
	`DELETE FROM reactions`,
	`DELETE FROM pins`,
	`DELETE FROM mentions`,
	`DELETE FROM attachments`,
	`DELETE FROM alerts`,
	`DELETE FROM messages`,
	`DELETE FROM files`,
	`DELETE FROM events`,
	`UPDATE memberships SET last_read_id = NULL WHERE last_read_id IS NOT NULL`,
	`INSERT INTO messages_fts (messages_fts) VALUES ('delete-all')`,
	`INSERT INTO messages_fts (messages_fts) VALUES ('optimize')`,
}

// clearContent deletes all content and moves the epoch to the marker's
// target in ONE writer transaction.
func clearContent(ctx context.Context, r *runner) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	return db.Do(ctx, func(tx *store.Tx) error {
		for _, q := range contentStatements {
			if _, err := tx.Exec(q); err != nil {
				return fmt.Errorf("clear content: %w", err)
			}
		}
		if _, err := tx.Exec(`UPDATE instance SET epoch = MAX(epoch, ?)`, r.m.Epoch); err != nil {
			return fmt.Errorf("bump epoch: %w", err)
		}
		return nil
	})
}

// vacuum truncates the WAL and rebuilds the database file so no deleted page
// survives in either (secure_delete has zeroed the freed pages already).
func vacuum(ctx context.Context, r *runner) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	return db.Maintenance(ctx, func(ctx context.Context, conn *sql.Conn) error {
		if err := checkpoint(ctx, conn); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `VACUUM`); err != nil {
			return fmt.Errorf("vacuum: %w", err)
		}
		return checkpoint(ctx, conn)
	})
}

func checkpoint(ctx context.Context, conn *sql.Conn) error {
	var busy, logFrames, done int
	if err := conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &done); err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	return nil
}

func trashFiles(_ context.Context, r *runner) error { return TrashFiles(r.dataDir) }

// wipeTmp empties the upload spool. It is best effort: a spool file still
// open by a cut-off upload (Windows refuses to delete it) is swept at the
// next start.
func wipeTmp(_ context.Context, r *runner) error {
	if err := clearDir(filepath.Join(r.dataDir, config.DirTmp)); err != nil {
		r.logger.Warn("reset: some temporary files remain", "err", err)
	}
	return nil
}

func deleteSnapshots(_ context.Context, r *runner) error { return removeSnapshots(r.dataDir) }

// release hands the database back: the server stops everything using it and
// closes it; offline the runner closes its own handle.
func release(ctx context.Context, r *runner) error {
	if r.hooks.Release != nil && !r.released {
		if err := r.hooks.Release(ctx); err != nil {
			return err
		}
	}
	r.released = true
	return r.closeDB()
}

func deleteDB(_ context.Context, r *runner) error {
	return removeFiles(r.dataDir, store.FileName, store.FileName+"-wal", store.FileName+"-shm", store.FileName+"-journal")
}

func deleteSecrets(_ context.Context, r *runner) error {
	return removeFiles(r.dataDir, push.VAPIDFile, auth.SetupTokenFile)
}

func wipeLogs(_ context.Context, r *runner) error {
	clear := func() error { return clearDir(filepath.Join(r.dataDir, config.DirLogs)) }
	if r.hooks.ReopenLogs != nil {
		return r.hooks.ReopenLogs(clear)
	}
	return clear()
}

func wipeBackups(_ context.Context, r *runner) error {
	return clearDir(filepath.Join(r.dataDir, config.DirBackups))
}

// rotateCA destroys the CA key and certificate (and the leaf) and, on a
// running server, creates and serves the new pair right away.
func rotateCA(_ context.Context, r *runner) error {
	if err := clearDir(filepath.Join(r.dataDir, config.DirTLS)); err != nil {
		return err
	}
	if r.hooks.NewCA != nil {
		return r.hooks.NewCA()
	}
	return nil
}

// openDB creates the fresh database: migrations and a new instance row.
func openDB(ctx context.Context, r *runner) error {
	_, err := r.database(ctx)
	return err
}

func restoreSettingsStep(ctx context.Context, r *runner) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	return restoreSettings(ctx, db, r.m.Settings)
}

// newVAPID creates the new Web Push key pair (old subscriptions die with
// the old keys).
func newVAPID(_ context.Context, r *runner) error {
	_, err := push.LoadOrCreateVAPID(r.dataDir)
	return err
}

func newSetupToken(_ context.Context, r *runner) error {
	token, err := auth.WriteSetupToken(r.dataDir)
	if err != nil {
		return err
	}
	r.token = token
	return nil
}

// auditFactory writes the first row of the new audit log. It names no
// account (the actor's account no longer exists).
func auditFactory(ctx context.Context, r *runner) error {
	db, err := r.database(ctx)
	if err != nil {
		return err
	}
	return db.Do(ctx, func(tx *store.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM audit_log WHERE action = ?`, ActionFactoryReset).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		actor := r.actor()
		actor.ID = ""
		_, err := audit.Write(tx, actor.Entry(ActionFactoryReset, "instance", "", map[string]bool{
			"rotate_ca": r.m.RotateCA, "keep_settings": r.m.KeepSettings,
		}))
		return err
	})
}

// restart closes the runner's own database and lets a running server
// rebuild on the new state.
func restart(ctx context.Context, r *runner) error {
	if err := r.closeDB(); err != nil {
		return err
	}
	if r.hooks.Restart == nil {
		return nil
	}
	return r.hooks.Restart(ctx)
}
