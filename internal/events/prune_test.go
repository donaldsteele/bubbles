package events

import (
	"context"
	"testing"

	"github.com/donaldsteele/bubbles/internal/store"
)

func TestPruneDeletesOnlyExpiredPrefix(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	old := testNow.Add(-Retention).UnixMilli() - 1
	fresh := testNow.Add(-Retention).UnixMilli() + 1
	const nOld, nNew = 1203, 5
	err := db.Do(ctx, func(tx *store.Tx) error {
		for i := range nOld + nNew {
			at := old
			if i >= nOld {
				at = fresh
			}
			if _, err := Append(tx, Event{Type: TypeSettingsUpdated, CreatedAt: at}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	n, err := Prune(ctx, db, testNow)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != nOld {
		t.Fatalf("pruned %d, want %d", n, nOld)
	}
	oldest, head, err := Bounds(ctx, db.Read())
	if err != nil {
		t.Fatal(err)
	}
	if oldest != nOld+1 || head != nOld+nNew {
		t.Fatalf("bounds = (%d, %d), want (%d, %d)", oldest, head, nOld+1, nOld+nNew)
	}
	evs, err := Range(ctx, db.Read(), 0, head, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != nNew || evs[0].Seq != nOld+1 || evs[0].Type != TypeSettingsUpdated || string(evs[0].Payload) != "{}" {
		t.Fatalf("Range = %+v", evs)
	}
	if n, err := Prune(ctx, db, testNow); err != nil || n != 0 {
		t.Fatalf("second Prune = %d, %v", n, err)
	}

	// Everything expired: the log is empty but the head survives.
	if _, err := Prune(ctx, db, testNow.Add(Retention+1)); err != nil {
		t.Fatal(err)
	}
	oldest, head, _ = Bounds(ctx, db.Read())
	if oldest != 0 || head != nOld+nNew {
		t.Fatalf("bounds after full prune = (%d, %d)", oldest, head)
	}
}
