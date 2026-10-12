package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

var testEpoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// openTestDB opens a fresh database in a temp dir and closes it at cleanup.
func openTestDB(t *testing.T, opts Options) *DB {
	t.Helper()
	if opts.Now == nil {
		opts.Now = fixedClock(testEpoch)
	}
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), FileName), opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenPragmas(t *testing.T) {
	db := openTestDB(t, Options{})
	ctx := context.Background()

	common := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"synchronous", "1"}, // NORMAL
		{"busy_timeout", "5000"},
		{"foreign_keys", "1"},
		{"temp_store", "2"}, // MEMORY
		{"secure_delete", "1"},
	}

	// Hold every read connection at once so each one is checked.
	for i := 0; i < readConns; i++ {
		conn, err := db.Read().Conn(ctx)
		if err != nil {
			t.Fatalf("read conn %d: %v", i, err)
		}
		defer conn.Close()
		for _, tc := range append(common, struct{ pragma, want string }{"query_only", "1"}) {
			var got string
			if err := conn.QueryRowContext(ctx, "PRAGMA "+tc.pragma).Scan(&got); err != nil {
				t.Fatalf("read conn %d: PRAGMA %s: %v", i, tc.pragma, err)
			}
			if got != tc.want {
				t.Errorf("read conn %d: PRAGMA %s = %q, want %q", i, tc.pragma, got, tc.want)
			}
		}
	}

	err := db.Do(ctx, func(tx *Tx) error {
		for _, tc := range append(common, struct{ pragma, want string }{"query_only", "0"}) {
			var got string
			if err := tx.QueryRow("PRAGMA " + tc.pragma).Scan(&got); err != nil {
				return err
			}
			if got != tc.want {
				t.Errorf("writer: PRAGMA %s = %q, want %q", tc.pragma, got, tc.want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
}

func TestReadPoolRejectsWrites(t *testing.T) {
	db := openTestDB(t, Options{})
	_, err := db.Read().Exec(`INSERT INTO settings (key, value) VALUES ('k', '1')`)
	if err == nil {
		t.Fatal("write through read pool succeeded, want error")
	}
}

func TestInstanceRowCreatedOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), FileName)
	opts := Options{Now: fixedClock(testEpoch)}

	readInstance := func(db *DB) (id string, epoch, createdAt int64) {
		t.Helper()
		var n int
		if err := db.Read().QueryRow(`SELECT count(*) FROM instance`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("instance rows = %d, want 1", n)
		}
		if err := db.Read().QueryRow(`SELECT id, epoch, created_at FROM instance`).Scan(&id, &epoch, &createdAt); err != nil {
			t.Fatal(err)
		}
		return id, epoch, createdAt
	}

	db, err := Open(ctx, path, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id, epoch, createdAt := readInstance(db)
	if _, err := ulid.ParseStrict(id); err != nil {
		t.Errorf("instance id %q is not a ULID: %v", id, err)
	}
	if epoch != 1 {
		t.Errorf("epoch = %d, want 1", epoch)
	}
	if createdAt != testEpoch.UnixMilli() {
		t.Errorf("created_at = %d, want %d", createdAt, testEpoch.UnixMilli())
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err = Open(ctx, path, Options{Now: fixedClock(testEpoch.Add(time.Hour))})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	id2, epoch2, createdAt2 := readInstance(db)
	if id2 != id || epoch2 != epoch || createdAt2 != createdAt {
		t.Errorf("instance changed on reopen: (%s,%d,%d) -> (%s,%d,%d)", id, epoch, createdAt, id2, epoch2, createdAt2)
	}
}

func TestCloseIdempotentAndDoAfterClose(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), FileName), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	ran := false
	err = db.Do(context.Background(), func(*Tx) error { ran = true; return nil })
	if !errors.Is(err, ErrClosed) {
		t.Errorf("Do after Close = %v, want ErrClosed", err)
	}
	if ran {
		t.Error("fn ran after Close")
	}
}

func TestOpenRejectsBadPath(t *testing.T) {
	for _, p := range []string{"", filepath.Join(t.TempDir(), "a?b.db")} {
		if db, err := Open(context.Background(), p, Options{}); err == nil {
			db.Close()
			t.Errorf("Open(%q) succeeded, want error", p)
		}
	}
}

func TestDatabaseFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits; Windows relies on the data directory DACL")
	}
	db := openTestDB(t, Options{})
	fi, err := os.Stat(db.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("database mode = %o, want 600", got)
	}
}

// TestReadPoolIsModeRO: the read pool opens the file with SQLite mode=ro, so
// even clearing query_only cannot make it write, and it still sees WAL commits.
func TestReadPoolIsModeRO(t *testing.T) {
	db := openTestDB(t, Options{})
	ctx := context.Background()
	if err := db.Do(ctx, func(tx *Tx) error {
		_, err := tx.Exec(`CREATE TABLE ro_probe (v TEXT)`)
		if err == nil {
			_, err = tx.Exec(`INSERT INTO ro_probe VALUES ('x')`)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Read().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var v string
	if err := conn.QueryRowContext(ctx, `SELECT v FROM ro_probe`).Scan(&v); err != nil || v != "x" {
		t.Fatalf("WAL read = %q, %v", v, err)
	}
	_, _ = conn.ExecContext(ctx, `PRAGMA query_only = 0`)
	if _, err := conn.ExecContext(ctx, `INSERT INTO ro_probe VALUES ('y')`); err == nil {
		t.Fatal("read connection accepted a write after query_only=0")
	}
}
