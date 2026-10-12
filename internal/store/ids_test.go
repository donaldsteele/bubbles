package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestIDsStayAheadOfPersistedAfterClockStepsBack(t *testing.T) {
	// The first run writes the newest ID into exactly one table; the second
	// run's clock is an hour earlier. Every table with ULID keys must count.
	tests := []struct {
		name   string
		seed   bool   // create the non-ULID rows the insert depends on
		insert string // takes the ULID as its only argument
	}{
		{"users", false, `INSERT INTO users (id, username, kind, role, created_at) VALUES (?, 'carol', 'human', 'member', 1)`},
		{"channels", false, `INSERT INTO channels (id, kind, name, created_at) VALUES (?, 'public', 'random', 1)`},
		{"audit_log", false, `INSERT INTO audit_log (id, at, actor_kind, action) VALUES (?, 1, 'system', 'test')`},
		{"messages", true, `INSERT INTO messages (id, channel_id, author_id, created_at) VALUES (?, 'c1', 'u1', 1)`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), FileName)

			db, err := Open(ctx, path, Options{Now: fixedClock(testEpoch)})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if tc.seed {
				seedBasics(t, db) // non-ULID ids ('u1', 'c1', ...) must not confuse seeding
			}
			var maxID string
			err = db.Do(ctx, func(tx *Tx) error {
				for i := 0; i < 20; i++ {
					maxID = tx.NewID()
				}
				_, err := tx.Exec(tc.insert, maxID)
				return err
			})
			if err != nil {
				t.Fatalf("write ids: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			db, err = Open(ctx, path, Options{Now: fixedClock(testEpoch.Add(-time.Hour))})
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			defer db.Close()
			var got []string
			err = db.Do(ctx, func(tx *Tx) error {
				for i := 0; i < 5; i++ {
					got = append(got, tx.NewID())
				}
				return nil
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			prev := maxID
			for i, id := range got {
				if id <= prev {
					t.Fatalf("id %d after reopen = %s, want > %s", i, id, prev)
				}
				prev = id
			}
		})
	}
}

func TestSeedIgnoresNonULIDIDs(t *testing.T) {
	db := openTestDB(t, Options{})
	seedBasics(t, db) // ids such as 'u1' and 'bot1' sort after any ULID
	g := newIDGen()
	if err := g.seedFromDB(context.Background(), db.write); err != nil {
		t.Fatalf("seedFromDB: %v", err)
	}
	// Only the instance row (stamped at testEpoch) holds a ULID.
	if want := uint64(testEpoch.UnixMilli()) + 1; g.lastMs != want {
		t.Errorf("lastMs = %d, want %d", g.lastMs, want)
	}
}
