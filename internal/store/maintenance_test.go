package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// TestMaintenanceRunsOutsideATransaction: VACUUM and wal_checkpoint, which
// SQLite refuses inside a transaction, run on the writer connection, and the
// writer keeps serving transactions afterwards.
func TestMaintenanceRunsOutsideATransaction(t *testing.T) {
	db := openTestDB(t, Options{Now: time.Now})
	ctx := context.Background()
	if err := db.Do(ctx, func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('k', '1')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var busy int
	err := db.Maintenance(ctx, func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `VACUUM`); err != nil {
			return err
		}
		var logFrames, done int
		return conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &done)
	})
	if err != nil || busy != 0 {
		t.Fatalf("Maintenance = %v (busy %d)", err, busy)
	}
	want := errors.New("boom")
	if err := db.Maintenance(ctx, func(context.Context, *sql.Conn) error { return want }); !errors.Is(err, want) {
		t.Errorf("error not returned: %v", err)
	}
	if err := db.Do(ctx, func(tx *Tx) error {
		_, err := tx.Exec(`UPDATE settings SET value = '2' WHERE key = 'k'`)
		return err
	}); err != nil {
		t.Fatalf("Do after Maintenance: %v", err)
	}
	db.Close()
	if err := db.Maintenance(ctx, func(context.Context, *sql.Conn) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Errorf("after Close: %v", err)
	}
}
