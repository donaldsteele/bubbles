package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/donaldsteele/bubbles/internal/config"
)

// SnapshotPrefix starts the file name of every pre-migration snapshot in the
// backups directory (pre-migrate-<from>-<to>.db).
const SnapshotPrefix = "pre-migrate-"

// snapshot writes a consistent copy of the database to
// <dir of dbPath>/backups/pre-migrate-<from>-<to>.db using VACUUM INTO and
// flushes it to disk. An existing snapshot with the same name is replaced:
// migrations are transactional, so a retry snapshots the same state.
func snapshot(ctx context.Context, db *sql.DB, dbPath string, from, to int) (string, error) {
	dir := filepath.Join(filepath.Dir(dbPath), config.DirBackups)
	if err := os.MkdirAll(dir, config.DirMode); err != nil {
		return "", fmt.Errorf("pre-migration snapshot: %w", err)
	}
	p := filepath.Join(dir, fmt.Sprintf("%s%d-%d.db", SnapshotPrefix, from, to))
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("pre-migration snapshot: %w", err)
	}
	// VACUUM INTO accepts an existing empty file, which lets us create it
	// with owner-only permissions first.
	if err := createPrivateFile(p); err != nil {
		return "", fmt.Errorf("pre-migration snapshot: %w", err)
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, p); err != nil {
		_ = os.Remove(p)
		return "", fmt.Errorf("pre-migration snapshot: %w", err)
	}
	if err := syncFile(p); err != nil {
		return "", fmt.Errorf("pre-migration snapshot: %w", err)
	}
	return p, nil
}

// syncFile flushes path to stable storage (SQLite does not fsync the output
// of VACUUM INTO).
func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
