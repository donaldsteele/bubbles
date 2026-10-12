// Package store owns the SQLite database file: opening it with the required
// pragmas, the read pool, the single writer goroutine, schema migrations and
// ID generation. It is infrastructure only; domain packages own their SQL.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/donaldsteele/bubbles/internal/config"

	// Registers the pure-Go "sqlite" database/sql driver.
	_ "modernc.org/sqlite"
)

// FileName is the database file inside the data directory.
const FileName = "bubbles.db"

// readConns is the size of the read connection pool.
const readConns = 8

// connPragmas run on every connection, writer and readers alike. busy_timeout
// comes first so the remaining pragmas wait for locks instead of failing.
var connPragmas = []string{
	"busy_timeout(5000)",
	"journal_mode(WAL)",
	"synchronous(NORMAL)",
	"foreign_keys(1)",
	"temp_store(MEMORY)",
	"secure_delete(1)",
}

// ErrClosed is returned by Do after Close has been called.
var ErrClosed = errors.New("database is closed")

// ErrFnExited is returned by Do when the transaction function (or an
// AfterCommit callback) ended its goroutine with runtime.Goexit, for example
// through testing.T.FailNow, without returning or panicking. The transaction
// is rolled back and the writer keeps serving later calls.
var ErrFnExited = errors.New("transaction function exited without returning")

// Options configures Open. The zero value is ready to use.
type Options struct {
	// Now is the clock behind Tx.Now, ULID timestamps and migration times.
	// Nil means time.Now.
	Now func() time.Time
	// Migrations replaces the embedded migrations (tests). Files named
	// NNNN_name.sql at the root of the FS, numbered 1..N without gaps.
	Migrations fs.FS
	// Logger receives migration and snapshot notices. Nil discards them.
	Logger *slog.Logger
}

// DB is an open database: one writer connection owned by a single goroutine
// (see Do) and a pool of read-only connections (see Read).
type DB struct {
	path  string
	write *sql.DB
	read  *sql.DB
	now   func() time.Time
	ids   *idGen // touched only by the writer goroutine

	reqs chan *request
	quit chan struct{}
	done chan struct{}

	values sync.Map // see SetValue

	closeOnce sync.Once
	closeErr  error
}

// SetValue attaches v to the database under key, like a context value: code
// that only holds a *Tx (event appenders, for example) finds it with
// Tx.Value. Keys should be unexported types owned by the package that sets
// them. A nil v is stored as is and reads back as nil.
func (db *DB) SetValue(key, v any) { db.values.Store(key, v) }

// Value returns the value set for key with SetValue, or nil.
func (db *DB) Value(key any) any {
	v, _ := db.values.Load(key)
	return v
}

// Open opens (creating if needed) the database at path, applies pending
// migrations, seeds the instance row and starts the writer goroutine.
//
// It refuses with an error matching ErrSchemaTooNew when the file was written
// by a newer binary, and snapshots a non-empty database into
// <dir>/backups/pre-migrate-<from>-<to>.db before migrating it.
func Open(ctx context.Context, path string, opts Options) (*DB, error) {
	if path == "" {
		return nil, errors.New("database path is empty")
	}
	if strings.ContainsRune(path, '?') {
		return nil, errors.New("database path must not contain '?'")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	migFS := opts.Migrations
	if migFS == nil {
		migFS = embeddedMigrations()
	}
	migs, err := loadMigrations(migFS)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(path), config.DirMode); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	if err := createPrivateFile(path); err != nil {
		return nil, fmt.Errorf("create database file: %w", err)
	}

	write, err := openPool(path, 1, false)
	if err != nil {
		return nil, err
	}
	if err := checkWAL(ctx, write); err != nil {
		write.Close()
		return nil, err
	}
	if err := migrate(ctx, write, migs, path, now, logger); err != nil {
		write.Close()
		return nil, err
	}

	read, err := openPool(path, readConns, true)
	if err != nil {
		write.Close()
		return nil, err
	}
	if err := read.PingContext(ctx); err != nil {
		read.Close()
		write.Close()
		return nil, fmt.Errorf("open read pool: %w", err)
	}

	ids := newIDGen()
	if err := ids.seedFromDB(ctx, write); err != nil {
		read.Close()
		write.Close()
		return nil, err
	}

	db := &DB{
		path:  path,
		write: write,
		read:  read,
		now:   now,
		ids:   ids,
		reqs:  make(chan *request),
		quit:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go db.loop()

	if err := db.ensureInstance(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Read returns the read-only connection pool (8 connections, query_only).
// Writes through it fail; use Do.
func (db *DB) Read() *sql.DB { return db.read }

// Path returns the database file path.
func (db *DB) Path() string { return db.path }

// Close stops accepting writes, waits for the in-flight write transaction to
// finish, then closes the writer connection and the read pool. It is safe to
// call more than once; later calls return the first result.
func (db *DB) Close() error {
	db.closeOnce.Do(func() {
		close(db.quit)
		<-db.done
		db.closeErr = errors.Join(db.read.Close(), db.write.Close())
	})
	return db.closeErr
}

// ensureInstance inserts the single instance row (new ULID, epoch 1) when the
// table is empty.
func (db *DB) ensureInstance(ctx context.Context) error {
	err := db.Do(ctx, func(tx *Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM instance`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		_, err := tx.Exec(`INSERT INTO instance (id, epoch, created_at) VALUES (?, 1, ?)`, tx.NewID(), tx.Now())
		return err
	})
	if err != nil {
		return fmt.Errorf("seed instance row: %w", err)
	}
	return nil
}

// openPool opens a database/sql handle whose every connection runs
// connPragmas. Read pools are additionally query_only; the writer begins
// transactions with BEGIN IMMEDIATE.
func openPool(path string, conns int, readOnly bool) (*sql.DB, error) {
	q := url.Values{}
	for _, p := range connPragmas {
		q.Add("_pragma", p)
	}
	dsn := path
	if readOnly {
		// mode=ro (SQLite URI) on top of query_only: the connection cannot
		// write even if a statement flips the pragma. WAL reads work because
		// the writer pool already created the -wal and -shm files.
		q.Add("_pragma", "query_only(1)")
		q.Set("mode", "ro")
		dsn = (&url.URL{Scheme: "file", Opaque: filepath.ToSlash(path)}).String()
		if len(path) >= 2 && path[1] == ':' {
			dsn = (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(path)}).String()
		}
	} else {
		q.Set("_txlock", "immediate")
	}
	db, err := sql.Open("sqlite", dsn+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	return db, nil
}

// checkWAL opens the first writer connection and confirms WAL mode took.
func checkWAL(ctx context.Context, db *sql.DB) error {
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("open database: journal_mode is %q, want wal", mode)
	}
	return nil
}

// createPrivateFile creates path if missing (never truncating) with owner-only
// permissions. On Windows the data directory's protected DACL is inherited.
func createPrivateFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, config.FileMode)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Chmod(path, config.FileMode)
	}
	return nil
}
