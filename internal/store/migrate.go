package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// embeddedMigrations returns the migrations compiled into the binary.
func embeddedMigrations() fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(fmt.Sprintf("store: embedded migrations: %v", err))
	}
	return sub
}

// ErrSchemaTooNew matches (errors.Is) the error Open returns when the database
// was migrated by a newer binary than this one.
var ErrSchemaTooNew = errors.New("database schema is newer than this binary")

// SchemaTooNewError reports the database and binary schema versions.
type SchemaTooNewError struct {
	DBVersion  int
	MaxVersion int
}

func (e *SchemaTooNewError) Error() string {
	return fmt.Sprintf("database schema version %d is newer than this binary supports (%d); run a newer bubbles release",
		e.DBVersion, e.MaxVersion)
}

// Is makes errors.Is(err, ErrSchemaTooNew) true.
func (e *SchemaTooNewError) Is(target error) bool { return target == ErrSchemaTooNew }

type migration struct {
	version int
	name    string
	sql     string
}

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// loadMigrations reads NNNN_name.sql files from the root of fsys. Versions
// must run 1..N without gaps or duplicates; other files are ignored.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var migs []migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		migs = append(migs, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	for i, m := range migs {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %s: expected version %04d (versions must run 1..N without gaps)", m.name, i+1)
		}
	}
	return migs, nil
}

// migrate brings the database up to the newest migration. Each migration and
// its schema_migrations row commit in one transaction. A database that
// already holds objects is snapshotted first.
func migrate(ctx context.Context, db *sql.DB, migs []migration, dbPath string, now func() time.Time, logger *slog.Logger) error {
	var objects int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE name != 'schema_migrations'`).Scan(&objects); err != nil {
		return fmt.Errorf("inspect database: %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	) STRICT`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	current, err := schemaVersion(ctx, db)
	if err != nil {
		return err
	}
	latest := len(migs)
	if current > latest {
		return &SchemaTooNewError{DBVersion: current, MaxVersion: latest}
	}
	if current == latest {
		return nil
	}

	if objects > 0 {
		p, err := snapshot(ctx, db, dbPath, current, latest)
		if err != nil {
			return err
		}
		logger.Info("pre-migration snapshot written", "path", p, "from", current, "to", latest)
	}
	for _, m := range migs[current:] {
		if err := applyMigration(ctx, db, m, now); err != nil {
			return err
		}
	}
	logger.Info("database migrated", "from", current, "to", latest)
	return nil
}

func schemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return v, nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration, now func() time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.version, now().UnixMilli()); err != nil {
		return fmt.Errorf("migration %s: record version: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %s: commit: %w", m.name, err)
	}
	return nil
}
