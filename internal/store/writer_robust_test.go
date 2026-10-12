package store

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// recv waits for one value from ch so a regression hangs for seconds, not for
// the whole test timeout.
func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func settingsRows(t *testing.T, db *DB, key string) int {
	t.Helper()
	var n int
	if err := db.Read().QueryRow(`SELECT count(*) FROM settings WHERE key = ?`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDoGoexitRollsBackAndWriterSurvives(t *testing.T) {
	db := openTestDB(t, Options{})
	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		done <- db.Do(ctx, func(tx *Tx) error {
			if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('goexit', '1')`); err != nil {
				return err
			}
			tx.AfterCommit(func() { t.Error("AfterCommit ran after Goexit") })
			runtime.Goexit() // what testing.T.FailNow does
			return nil
		})
	}()
	if err := recv(t, done, "Do after Goexit"); !errors.Is(err, ErrFnExited) {
		t.Fatalf("Do = %v, want ErrFnExited", err)
	}
	if n := settingsRows(t, db, "goexit"); n != 0 {
		t.Errorf("row from exited transaction was committed (%d rows)", n)
	}

	// The writer goroutine and the write connection are still usable.
	go func() {
		done <- db.Do(ctx, func(tx *Tx) error {
			_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('goexit', '1')`)
			return err
		})
	}()
	if err := recv(t, done, "Do after recovery"); err != nil {
		t.Fatalf("Do after Goexit: %v", err)
	}
	if n := settingsRows(t, db, "goexit"); n != 1 {
		t.Errorf("rows after recovery = %d, want 1", n)
	}
}

func TestAfterCommitGoexitKeepsWriterAlive(t *testing.T) {
	db := openTestDB(t, Options{})
	ctx := context.Background()

	var ran []int
	done := make(chan error, 1)
	go func() {
		done <- db.Do(ctx, func(tx *Tx) error {
			tx.AfterCommit(func() { ran = append(ran, 1) })
			tx.AfterCommit(func() { runtime.Goexit() })
			tx.AfterCommit(func() { ran = append(ran, 3) })
			_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('ac-exit', '1')`)
			return err
		})
	}()
	if err := recv(t, done, "Do"); !errors.Is(err, ErrFnExited) {
		t.Fatalf("Do = %v, want ErrFnExited", err)
	}
	if fmt.Sprint(ran) != "[1 3]" {
		t.Errorf("callbacks ran %v, want [1 3]", ran)
	}
	if n := settingsRows(t, db, "ac-exit"); n != 1 {
		t.Errorf("committed rows = %d, want 1", n)
	}
	if err := db.Do(ctx, func(*Tx) error { return nil }); err != nil {
		t.Fatalf("Do after callback Goexit: %v", err)
	}
}

func TestCloseWithQueuedRequests(t *testing.T) {
	db := openTestDB(t, Options{})
	ctx := context.Background()

	started, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- db.Do(ctx, func(*Tx) error {
			close(started)
			<-release
			return nil
		})
	}()
	recv(t, started, "first transaction to start")

	const queued = 16
	var ran atomic.Int32
	queuedErrs := make(chan error, queued)
	for i := 0; i < queued; i++ {
		go func() {
			queuedErrs <- db.Do(ctx, func(*Tx) error { ran.Add(1); return nil })
		}()
	}
	closeErr := make(chan error, 1)
	go func() { closeErr <- db.Close() }()
	recv(t, db.quit, "Close to begin") // quit is closed, so this receive succeeds once Close started

	close(release)
	if err := recv(t, first, "in-flight Do"); err != nil {
		t.Errorf("in-flight Do = %v, want nil (it was already running)", err)
	}
	for i := 0; i < queued; i++ {
		if err := recv(t, queuedErrs, "queued Do"); !errors.Is(err, ErrClosed) {
			t.Errorf("queued Do = %v, want ErrClosed", err)
		}
	}
	if err := recv(t, closeErr, "Close"); err != nil {
		t.Errorf("Close: %v", err)
	}
	if n := ran.Load(); n != 0 {
		t.Errorf("%d queued functions ran after Close, want 0", n)
	}
}

func TestAfterCommitPanicReraisedAfterCommit(t *testing.T) {
	db := openTestDB(t, Options{})
	ctx := context.Background()

	var ran []int
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = db.Do(ctx, func(tx *Tx) error {
			tx.AfterCommit(func() { ran = append(ran, 1) })
			tx.AfterCommit(func() { panic("after-boom") })
			tx.AfterCommit(func() { ran = append(ran, 3) })
			tx.AfterCommit(func() { panic("second-boom") })
			_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('ac-panic', '1')`)
			return err
		})
	}()
	if recovered != "after-boom" {
		t.Errorf("recovered %v, want the first panic (after-boom)", recovered)
	}
	if fmt.Sprint(ran) != "[1 3]" {
		t.Errorf("callbacks ran %v, want [1 3] (remaining callbacks still run)", ran)
	}
	if n := settingsRows(t, db, "ac-panic"); n != 1 {
		t.Errorf("committed rows = %d, want 1 (panic happens after commit)", n)
	}
	if err := db.Do(ctx, func(*Tx) error { return nil }); err != nil {
		t.Fatalf("Do after callback panic: %v", err)
	}
}

func TestDoContextCanceledDuringFn(t *testing.T) {
	db := openTestDB(t, Options{})

	tests := []struct {
		name string
		fn   func(cancel context.CancelFunc, tx *Tx) error
	}{
		{"fn returns the statement error", func(cancel context.CancelFunc, tx *Tx) error {
			cancel()
			_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('cancel', '1')`)
			return err
		}},
		{"fn ignores cancellation", func(cancel context.CancelFunc, tx *Tx) error {
			if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('cancel', '1')`); err != nil {
				return err
			}
			cancel()
			return nil
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := db.Do(ctx, func(tx *Tx) error { return tc.fn(cancel, tx) })
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Do = %v, want context.Canceled", err)
			}
			if n := settingsRows(t, db, "cancel"); n != 0 {
				t.Errorf("canceled transaction left %d rows, want 0", n)
			}
			if err := db.Do(context.Background(), func(*Tx) error { return nil }); err != nil {
				t.Fatalf("Do after cancellation: %v", err)
			}
		})
	}
}
