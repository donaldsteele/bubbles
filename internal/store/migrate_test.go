package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
)

func initSQL(t *testing.T) []byte {
	t.Helper()
	b, err := fs.ReadFile(embeddedMigrations(), "0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEmbeddedMigrations(t *testing.T) {
	migs, err := loadMigrations(embeddedMigrations())
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migs) == 0 || migs[0].name != "0001_init.sql" {
		t.Fatalf("embedded migrations = %v, want 0001_init.sql first", migs)
	}
}

func TestLoadMigrations(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	tests := []struct {
		name     string
		fsys     fstest.MapFS
		versions []int
		wantErr  bool
	}{
		{"empty", fstest.MapFS{}, nil, false},
		{"ordered", fstest.MapFS{"0002_b.sql": file("B"), "0001_a.sql": file("A")}, []int{1, 2}, false},
		{"ignores other files", fstest.MapFS{"0001_a.sql": file("A"), "README.md": file(""), "1_bad.sql": file("")}, []int{1}, false},
		{"gap", fstest.MapFS{"0001_a.sql": file("A"), "0003_c.sql": file("C")}, nil, true},
		{"duplicate", fstest.MapFS{"0001_a.sql": file("A"), "0001_b.sql": file("B")}, nil, true},
		{"starts above one", fstest.MapFS{"0002_b.sql": file("B")}, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			migs, err := loadMigrations(tc.fsys)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if len(migs) != len(tc.versions) {
				t.Fatalf("got %d migrations, want %d", len(migs), len(tc.versions))
			}
			for i, m := range migs {
				if m.version != tc.versions[i] {
					t.Errorf("migration %d version = %d, want %d", i, m.version, tc.versions[i])
				}
			}
		})
	}
}

func appliedVersions(t *testing.T, db *sql.DB) map[int]int64 {
	t.Helper()
	rows, err := db.Query(`SELECT version, applied_at FROM schema_migrations`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[int]int64{}
	for rows.Next() {
		var v int
		var at int64
		if err := rows.Scan(&v, &at); err != nil {
			t.Fatal(err)
		}
		got[v] = at
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMigrateTwiceIsNoop(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	db, err := Open(ctx, path, Options{Now: fixedClock(testEpoch)})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	first := appliedVersions(t, db.Read())
	migs, _ := loadMigrations(embeddedMigrations())
	if len(first) != len(migs) {
		t.Fatalf("applied %d migrations, want %d", len(first), len(migs))
	}
	// Running the migrator again on the live writer changes nothing.
	if err := migrate(ctx, db.write, migs, path, fixedClock(testEpoch.Add(1)), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	db.Close()

	db, err = Open(ctx, path, Options{Now: fixedClock(testEpoch.Add(1e9))})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	second := appliedVersions(t, db.Read())
	if len(second) != len(first) {
		t.Fatalf("versions after reopen = %v, want %v", second, first)
	}
	for v, at := range first {
		if second[v] != at {
			t.Errorf("version %d applied_at changed: %d -> %d", v, at, second[v])
		}
	}
	// A brand-new database is never snapshotted, and nothing was pending.
	if _, err := os.Stat(filepath.Join(dir, "backups")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("backups dir exists (err=%v), want no snapshot", err)
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), FileName)
	db, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = db.Do(ctx, func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (999, 0)`)
		return err
	})
	if err != nil {
		t.Fatalf("insert future version: %v", err)
	}
	db.Close()

	db, err = Open(ctx, path, Options{})
	if err == nil {
		db.Close()
		t.Fatal("Open succeeded on a newer schema, want error")
	}
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("err = %v, want ErrSchemaTooNew", err)
	}
	var tooNew *SchemaTooNewError
	if !errors.As(err, &tooNew) || tooNew.DBVersion != 999 || tooNew.MaxVersion != 3 {
		t.Errorf("err = %#v, want SchemaTooNewError{999, 2}", err)
	}
}

func TestPreMigrationSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	v1 := fstest.MapFS{"0001_init.sql": {Data: initSQL(t)}}

	db, err := Open(ctx, path, Options{Migrations: v1, Now: fixedClock(testEpoch)})
	if err != nil {
		t.Fatalf("Open v1: %v", err)
	}
	err = db.Do(ctx, func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('instance_name', '"home"')`)
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	db.Close()

	v2 := fstest.MapFS{
		"0001_init.sql":  v1["0001_init.sql"],
		"0002_extra.sql": {Data: []byte(`CREATE TABLE extra (id INTEGER PRIMARY KEY) STRICT;`)},
	}
	db, err = Open(ctx, path, Options{Migrations: v2, Now: fixedClock(testEpoch)})
	if err != nil {
		t.Fatalf("Open v2: %v", err)
	}
	defer db.Close()
	if got := appliedVersions(t, db.Read()); len(got) != 2 {
		t.Fatalf("versions = %v, want 1 and 2", got)
	}

	snap := filepath.Join(dir, "backups", SnapshotPrefix+"1-2.db")
	fi, err := os.Stat(snap)
	if err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("snapshot mode = %o, want 600", fi.Mode().Perm())
	}

	// The snapshot holds the pre-migration state: version 1, data, no extra.
	sdb, err := sql.Open("sqlite", snap+"?_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	if got := appliedVersions(t, sdb); len(got) != 1 {
		t.Errorf("snapshot versions = %v, want only 1", got)
	}
	var name string
	if err := sdb.QueryRow(`SELECT value FROM settings WHERE key = 'instance_name'`).Scan(&name); err != nil || name != `"home"` {
		t.Errorf("snapshot setting = %q, %v; want \"home\"", name, err)
	}
	var n int
	if err := sdb.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'extra'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("snapshot has extra table (n=%d, err=%v)", n, err)
	}
}

func TestDisplayKeyMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), FileName)
	v1 := fstest.MapFS{"0001_init.sql": {Data: initSQL(t)}}
	db, err := Open(ctx, path, Options{Migrations: v1, Now: fixedClock(testEpoch)})
	if err != nil {
		t.Fatalf("Open v1: %v", err)
	}
	err = db.Do(ctx, func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO users (id, username, display_name, kind, role, created_at) VALUES ('u1', 'ada', 'Ada', 'human', 'member', 1)`)
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	db.Close()

	db, err = Open(ctx, path, Options{Now: fixedClock(testEpoch)})
	if err != nil {
		t.Fatalf("Open latest: %v", err)
	}
	defer db.Close()
	var key sql.NullString
	if err := db.Read().QueryRow(`SELECT display_key FROM users WHERE id = 'u1'`).Scan(&key); err != nil || key.Valid {
		t.Errorf("display_key = %v, %v; want NULL for a pre-existing row", key, err)
	}
	var idx string
	if err := db.Read().QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'users_display_key'`).Scan(&idx); err != nil {
		t.Errorf("users_display_key index: %v", err)
	}
}

func TestUpgradeKeepsSignupClosed(t *testing.T) {
	ctx := context.Background()
	v1 := fstest.MapFS{"0001_init.sql": {Data: initSQL(t)}}
	open := func(path string, seed string) *DB {
		t.Helper()
		db, err := Open(ctx, path, Options{Migrations: v1, Now: fixedClock(testEpoch)})
		if err != nil {
			t.Fatalf("Open v1: %v", err)
		}
		if seed != "" {
			if err := db.Do(ctx, func(tx *Tx) error { _, err := tx.Exec(seed); return err }); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
		db.Close()
		db, err = Open(ctx, path, Options{Now: fixedClock(testEpoch)})
		if err != nil {
			t.Fatalf("Open latest: %v", err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	setting := func(db *DB) (string, bool) {
		var v string
		err := db.Read().QueryRow(`SELECT value FROM settings WHERE key = 'open_registration'`).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false
		}
		if err != nil {
			t.Fatal(err)
		}
		return v, true
	}
	user := `INSERT INTO users (id, username, display_name, kind, role, created_at) VALUES ('u1', 'ada', 'Ada', 'human', 'admin', 1)`

	// An upgraded v0.1.x server with users stays invite-only.
	if v, ok := setting(open(filepath.Join(t.TempDir(), FileName), user)); !ok || v != "false" {
		t.Errorf("upgraded server: open_registration = %q (stored %v), want false", v, ok)
	}
	// A choice the admin already made is kept.
	chose := user + `; INSERT INTO settings (key, value) VALUES ('open_registration', 'true')`
	if v, ok := setting(open(filepath.Join(t.TempDir(), FileName), chose)); !ok || v != "true" {
		t.Errorf("admin choice: open_registration = %q (stored %v), want true", v, ok)
	}
	// A server without users (new, or factory reset) keeps the default.
	if v, ok := setting(open(filepath.Join(t.TempDir(), FileName), "")); ok {
		t.Errorf("new server: open_registration stored as %q, want the default", v)
	}
}
