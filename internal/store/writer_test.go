package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

func TestConcurrentWritesSerialize(t *testing.T) {
	db := openTestDB(t, Options{Now: time.Now})
	ctx := context.Background()
	const writers, perWriter = 100, 50

	var (
		mu  sync.Mutex
		ids []string // commit order
	)
	stopReads := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stopReads:
					return
				default:
				}
				var n int
				if err := db.Read().QueryRow(`SELECT count(*) FROM settings`).Scan(&n); err != nil {
					t.Errorf("concurrent read: %v", err)
					return
				}
			}
		}()
	}

	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				err := db.Do(ctx, func(tx *Tx) error {
					id := tx.NewID()
					if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, id, fmt.Sprint(tx.Now())); err != nil {
						return err
					}
					tx.AfterCommit(func() {
						mu.Lock()
						ids = append(ids, id)
						mu.Unlock()
					})
					return nil
				})
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(stopReads)
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Do: %v", err) // includes any SQLITE_BUSY
	}

	var n int
	if err := db.Read().QueryRow(`SELECT count(*) FROM settings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != writers*perWriter {
		t.Fatalf("rows = %d, want %d", n, writers*perWriter)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != writers*perWriter {
		t.Fatalf("AfterCommit ran %d times, want %d", len(ids), writers*perWriter)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("ids not increasing in commit order at %d: %s <= %s", i, ids[i], ids[i-1])
		}
	}
}

func TestNewIDStrictlyIncreasing(t *testing.T) {
	// The clock jumps backwards between transactions; IDs must still rise and
	// their timestamps must never go back.
	var offset atomic.Int64
	clock := func() time.Time { return testEpoch.Add(time.Duration(offset.Load())) }
	db := openTestDB(t, Options{Now: clock})
	ctx := context.Background()

	steps := []time.Duration{0, -time.Second, -time.Minute, 2 * time.Millisecond, 0, -time.Hour, time.Hour}
	var all []string
	for _, step := range steps {
		offset.Store(int64(step))
		err := db.Do(ctx, func(tx *Tx) error {
			for i := 0; i < 200; i++ {
				all = append(all, tx.NewID())
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
	}
	var lastMs uint64
	for i, s := range all {
		id, err := ulid.ParseStrict(s)
		if err != nil {
			t.Fatalf("id %d %q: %v", i, s, err)
		}
		if i > 0 && s <= all[i-1] {
			t.Fatalf("id %d %s <= previous %s", i, s, all[i-1])
		}
		if id.Time() < lastMs {
			t.Fatalf("id %d timestamp went backwards", i)
		}
		lastMs = id.Time()
	}
}

func TestAfterCommit(t *testing.T) {
	db := openTestDB(t, Options{})
	ctx := context.Background()
	errBoom := errors.New("boom")

	tests := []struct {
		name    string
		fnErr   error
		wantErr error
		wantRun []int
		wantRow bool
	}{
		{"commit runs callbacks in order", nil, nil, []int{1, 2, 3}, true},
		{"rollback skips callbacks", errBoom, errBoom, nil, false},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key := fmt.Sprintf("after-%d", i)
			var ran []int
			err := db.Do(ctx, func(tx *Tx) error {
				for _, n := range []int{1, 2, 3} {
					tx.AfterCommit(func() { ran = append(ran, n) })
				}
				if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, '1')`, key); err != nil {
					return err
				}
				return tc.fnErr
			})
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("Do = %v, want %v", err, tc.wantErr)
			}
			if fmt.Sprint(ran) != fmt.Sprint(tc.wantRun) {
				t.Errorf("callbacks ran %v, want %v", ran, tc.wantRun)
			}
			var n int
			if err := db.Read().QueryRow(`SELECT count(*) FROM settings WHERE key = ?`, key).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if (n == 1) != tc.wantRow {
				t.Errorf("row present = %v, want %v", n == 1, tc.wantRow)
			}
		})
	}
}

func TestDoContextCanceledBeforeRun(t *testing.T) {
	db := openTestDB(t, Options{})

	t.Run("already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ran := false
		if err := db.Do(ctx, func(*Tx) error { ran = true; return nil }); !errors.Is(err, context.Canceled) {
			t.Errorf("Do = %v, want context.Canceled", err)
		}
		if ran {
			t.Error("fn ran with a canceled context")
		}
	})

	t.Run("canceled while queued", func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		first := make(chan error, 1)
		go func() {
			first <- db.Do(context.Background(), func(*Tx) error {
				close(started)
				<-release
				return nil
			})
		}()
		<-started

		ctx, cancel := context.WithCancel(context.Background())
		var ran atomic.Bool
		second := make(chan error, 1)
		go func() { second <- db.Do(ctx, func(*Tx) error { ran.Store(true); return nil }) }()
		cancel()
		if err := <-second; !errors.Is(err, context.Canceled) {
			t.Errorf("queued Do = %v, want context.Canceled", err)
		}
		close(release)
		if err := <-first; err != nil {
			t.Errorf("first Do: %v", err)
		}
		if ran.Load() {
			t.Error("queued fn ran after cancellation")
		}
	})
}

func TestDoPanicPropagatesAndRollsBack(t *testing.T) {
	db := openTestDB(t, Options{})
	ctx := context.Background()

	func() {
		defer func() {
			if v := recover(); v != "kaboom" {
				t.Errorf("recovered %v, want kaboom", v)
			}
		}()
		_ = db.Do(ctx, func(tx *Tx) error {
			if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('p', '1')`); err != nil {
				return err
			}
			panic("kaboom")
		})
	}()

	var n int
	if err := db.Read().QueryRow(`SELECT count(*) FROM settings WHERE key = 'p'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("row from panicking transaction was committed")
	}
	// The writer goroutine survived.
	if err := db.Do(ctx, func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('p', '1')`)
		return err
	}); err != nil {
		t.Fatalf("Do after panic: %v", err)
	}
}

func TestTxNowIsFixedClock(t *testing.T) {
	db := openTestDB(t, Options{Now: fixedClock(testEpoch)})
	var got int64
	if err := db.Do(context.Background(), func(tx *Tx) error { got = tx.Now(); return nil }); err != nil {
		t.Fatal(err)
	}
	if got != testEpoch.UnixMilli() {
		t.Errorf("Now = %d, want %d", got, testEpoch.UnixMilli())
	}
}
