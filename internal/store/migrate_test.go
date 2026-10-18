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
	"strings"
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
	if !errors.As(err, &tooNew) || tooNew.DBVersion != 999 || tooNew.MaxVersion != 4 {
		t.Errorf("err = %#v, want SchemaTooNewError{999, 4}", err)
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

// migrationsUpTo returns the embedded migrations 0001..n.
func migrationsUpTo(t *testing.T, n int) fstest.MapFS {
	t.Helper()
	migs, err := loadMigrations(embeddedMigrations())
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, m := range migs[:n] {
		out[m.name] = &fstest.MapFile{Data: []byte(m.sql)}
	}
	return out
}

func TestPairingRequestsRebuildMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), FileName)
	db, err := Open(ctx, path, Options{Migrations: migrationsUpTo(t, 3), Now: fixedClock(testEpoch)})
	if err != nil {
		t.Fatalf("Open v3: %v", err)
	}
	seedBasics(t, db)
	fixture(t, db, `INSERT INTO pairing_requests (id, app_name, description, requested_scopes, code, poll_secret_hash, source_ip,
			state, approved_app_id, created_at, expires_at) VALUES
		('p1', 'netdiag', 'sends alerts', '["alerts:write"]', '123456', X'01', '192.168.1.9', 'pending', NULL, 10, 20),
		('p2', 'monitor', '', '["mcp"]', '654321', X'02', '192.168.1.10', 'approved', 'a1', 11, 21),
		('p3', 'old', '', '[]', '123456', X'03', '', 'claimed', 'a1', 12, 22)`)
	db.Close()

	db, err = Open(ctx, path, Options{Now: fixedClock(testEpoch)})
	if err != nil {
		t.Fatalf("Open latest: %v", err)
	}
	defer db.Close()

	// Every row survived with its values, and the new columns have their defaults.
	type row struct {
		id, name, desc, scopes, code, ip, state string
		hash                                    []byte
		approved, replaces                      sql.NullString
		replaced                                int
		created, expires                        int64
	}
	rows, err := db.Read().Query(`SELECT id, app_name, description, requested_scopes, code, poll_secret_hash, source_ip, state,
		approved_app_id, replaces_app_id, replaced, created_at, expires_at FROM pairing_requests ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.name, &r.desc, &r.scopes, &r.code, &r.hash, &r.ip, &r.state, &r.approved, &r.replaces,
			&r.replaced, &r.created, &r.expires); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	rows.Close()
	if len(got) != 3 {
		t.Fatalf("rows after migration = %d, want 3", len(got))
	}
	if r := got[0]; r.id != "p1" || r.name != "netdiag" || r.desc != "sends alerts" || r.scopes != `["alerts:write"]` || r.code != "123456" ||
		string(r.hash) != "\x01" || r.ip != "192.168.1.9" || r.state != "pending" || r.approved.Valid || r.created != 10 || r.expires != 20 {
		t.Errorf("p1 = %+v", r)
	}
	if r := got[1]; r.state != "approved" || r.approved.String != "a1" || r.scopes != `["mcp"]` {
		t.Errorf("p2 = %+v", r)
	}
	for _, r := range got {
		if r.replaces.Valid || r.replaced != 0 {
			t.Errorf("%s: replaces_app_id = %v, replaced = %d; want NULL and 0", r.id, r.replaces, r.replaced)
		}
	}

	// The new state and columns are accepted; the CHECKs still hold.
	fixture(t, db, `UPDATE pairing_requests SET state = 'cancelled' WHERE id = 'p1'`)
	fixture(t, db, `UPDATE pairing_requests SET replaces_app_id = 'a1', replaced = 1 WHERE id = 'p2'`)
	for name, q := range map[string]string{
		"unknown state":       `UPDATE pairing_requests SET state = 'bogus' WHERE id = 'p2'`,
		"replaced not a bool": `UPDATE pairing_requests SET replaced = 2 WHERE id = 'p2'`,
		"unknown app":         `UPDATE pairing_requests SET replaces_app_id = 'nope' WHERE id = 'p2'`,
	} {
		if err := execErr(db, q); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// The partial unique index on the pending code is back.
	var idx string
	if err := db.Read().QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'pairing_requests_pending_code'`).Scan(&idx); err != nil {
		t.Fatalf("pairing_requests_pending_code: %v", err)
	}
	if !strings.Contains(idx, "WHERE state = 'pending'") {
		t.Errorf("index = %s", idx)
	}
	fixture(t, db, `INSERT INTO pairing_requests (id, app_name, code, poll_secret_hash, created_at, expires_at) VALUES ('p4', 'x', '777777', X'04', 1, 2)`)
	if err := execErr(db, `INSERT INTO pairing_requests (id, app_name, code, poll_secret_hash, created_at, expires_at) VALUES ('p5', 'y', '777777', X'05', 1, 2)`); err == nil {
		t.Error("two pending requests share a code")
	}

	// The foreign keys hold, and deleting the app clears both references.
	var violations int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check('pairing_requests')`).Scan(&violations); err != nil || violations != 0 {
		t.Errorf("foreign key violations = %d, %v", violations, err)
	}
	fixture(t, db, `DELETE FROM apps WHERE id = 'a1'`)
	var approved, replaces sql.NullString
	if err := db.Read().QueryRow(`SELECT approved_app_id, replaces_app_id FROM pairing_requests WHERE id = 'p2'`).Scan(&approved, &replaces); err != nil ||
		approved.Valid || replaces.Valid {
		t.Errorf("after deleting the app: approved %v, replaces %v, %v", approved, replaces, err)
	}
	var leftover int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'pairing_requests_new'`).Scan(&leftover); err != nil || leftover != 0 {
		t.Errorf("pairing_requests_new left behind: %d, %v", leftover, err)
	}
}
