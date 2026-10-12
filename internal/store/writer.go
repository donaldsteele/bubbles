package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// request is one write transaction queued for the writer goroutine.
type request struct {
	ctx context.Context
	fn  func(tx *Tx) error
	// raw, when set instead of fn, runs on the bare write connection outside
	// any transaction (see Maintenance).
	raw  func(ctx context.Context, conn *sql.Conn) error
	done chan result
}

// result carries the outcome back to Do. A panic inside fn or an AfterCommit
// callback is recovered in the writer goroutine and re-raised in the caller.
type result struct {
	err      error
	panicked bool
	panicVal any
}

// Tx is a write transaction handed to the function passed to Do. It exposes
// the query methods of *sql.Tx; commit and rollback belong to the writer.
// A Tx must not be used after its function returns.
type Tx struct {
	db    *DB
	tx    *sql.Tx
	ctx   context.Context
	ids   *idGen
	now   time.Time
	after []func()
}

// Do runs fn inside one write transaction on the single writer goroutine.
// Calls are serialized in arrival order. The transaction commits when fn
// returns nil and rolls back otherwise; fn's error is returned unchanged.
//
// If ctx is done before the transaction starts, Do returns ctx.Err() without
// running fn. Cancelling ctx while fn runs aborts the transaction. fn must not
// call Do (it would deadlock). After Close, Do returns ErrClosed. A panic in
// fn rolls back and is re-raised in the caller; an fn that ends its goroutine
// with runtime.Goexit rolls back and makes Do return ErrFnExited.
func (db *DB) Do(ctx context.Context, fn func(tx *Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	req := &request{ctx: ctx, fn: fn, done: make(chan result, 1)}
	select {
	case db.reqs <- req:
	case <-db.quit:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	res := <-req.done
	if res.panicked {
		panic(res.panicVal)
	}
	return res.err
}

// loop is the writer goroutine: it owns the write connection and the ID
// generator and executes requests one at a time until Close.
func (db *DB) loop() {
	defer close(db.done)
	for {
		select {
		case <-db.quit:
			return
		default:
		}
		select {
		case <-db.quit:
			return
		case req := <-db.reqs:
			// Close wins over a request accepted in the same instant, so no
			// request starts after Close has been called.
			select {
			case <-db.quit:
				req.done <- result{err: ErrClosed}
				return
			default:
			}
			req.done <- db.run(req)
		}
	}
}

// run executes one request: begin, fn, commit or rollback, then the
// AfterCommit callbacks in registration order.
func (db *DB) run(req *request) result {
	if err := req.ctx.Err(); err != nil {
		return result{err: err}
	}
	if req.raw != nil {
		return db.runRaw(req)
	}
	sqlTx, err := db.write.BeginTx(req.ctx, nil)
	if err != nil {
		return result{err: fmt.Errorf("begin write transaction: %w", err)}
	}
	tx := &Tx{db: db, tx: sqlTx, ctx: req.ctx, ids: db.ids, now: db.now()}

	res := callFn(tx, req.fn)
	if res.panicked || res.err != nil {
		_ = sqlTx.Rollback()
		return res
	}
	// A transaction whose context ended while fn ran never commits, even if
	// fn ignored the cancellation.
	if err := req.ctx.Err(); err != nil {
		_ = sqlTx.Rollback()
		return result{err: err}
	}
	if err := sqlTx.Commit(); err != nil {
		if ctxErr := req.ctx.Err(); ctxErr != nil {
			return result{err: ctxErr} // database/sql rolled back on cancellation
		}
		return result{err: fmt.Errorf("commit write transaction: %w", err)}
	}

	var first result
	for _, f := range tx.after {
		r := callAfter(f)
		if (r.panicked && !first.panicked) || (r.err != nil && first.err == nil && !first.panicked) {
			first = r
		}
	}
	return first
}

// callFn runs the transaction function on a short-lived goroutine owned by the
// writer. The writer goroutine must survive anything fn does: a panic is
// captured for re-raising in the caller, and runtime.Goexit (t.FailNow, for
// instance) is reported as ErrFnExited instead of ending the goroutine.
func callFn(tx *Tx, fn func(*Tx) error) result {
	var err error
	res := protect(func() { err = fn(tx) })
	if res.err == nil && !res.panicked {
		res.err = err
	}
	return res
}

// callAfter runs one AfterCommit callback with the same protection as callFn.
func callAfter(f func()) result { return protect(f) }

// protect runs f on a new goroutine and waits for it. It reports a panic in f
// as result.panicked and an f that neither returned nor panicked (Goexit) as
// result.err = ErrFnExited.
func protect(f func()) result {
	out := make(chan result, 1)
	go func() {
		finished := false
		defer func() {
			if finished {
				out <- result{}
				return
			}
			if v := recover(); v != nil {
				out <- result{panicked: true, panicVal: v}
				return
			}
			out <- result{err: ErrFnExited}
		}()
		f()
		finished = true
	}()
	return <-out
}

// NewID returns a new ULID. IDs are strictly increasing across all
// transactions (commit order), even if the clock steps backwards.
func (t *Tx) NewID() string { return t.ids.next(t.now) }

// Now returns the transaction's timestamp in unix milliseconds. It is read
// once from the clock when the transaction starts, so every row written by
// one transaction shares it.
func (t *Tx) Now() int64 { return t.now.UnixMilli() }

// AfterCommit registers f to run on the writer goroutine after a successful
// commit, in registration order (so callbacks observe global commit order).
// It never runs after a rollback. f must be quick and must not call Do.
func (t *Tx) AfterCommit(f func()) { t.after = append(t.after, f) }

// Value returns the database value set for key with DB.SetValue, or nil.
func (t *Tx) Value(key any) any { return t.db.Value(key) }

// Context returns the context passed to Do.
func (t *Tx) Context() context.Context { return t.ctx }

// Exec executes a statement using the Do context.
func (t *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(t.ctx, query, args...)
}

// Query runs a query using the Do context.
func (t *Tx) Query(query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(t.ctx, query, args...)
}

// QueryRow runs a single-row query using the Do context.
func (t *Tx) QueryRow(query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(t.ctx, query, args...)
}

// ExecContext executes a statement.
func (t *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

// QueryContext runs a query.
func (t *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, query, args...)
}

// QueryRowContext runs a single-row query.
func (t *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, query, args...)
}

// PrepareContext prepares a statement scoped to the transaction (batch
// inserts and deletes).
func (t *Tx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return t.tx.PrepareContext(ctx, query)
}
