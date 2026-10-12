package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Maintenance runs fn on the writer goroutine with the bare write connection,
// outside any transaction, so it can run statements SQLite refuses inside one
// (VACUUM, PRAGMA wal_checkpoint). No write transaction runs while fn does;
// readers keep their own connections. fn must not call Do or Maintenance (it
// would deadlock) and must leave the connection outside a transaction.
//
// The cancellation, panic and Goexit rules of Do apply. After Close it
// returns ErrClosed.
func (db *DB) Maintenance(ctx context.Context, fn func(ctx context.Context, conn *sql.Conn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	req := &request{ctx: ctx, raw: fn, done: make(chan result, 1)}
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

// runRaw executes a Maintenance request on the writer's only connection.
func (db *DB) runRaw(req *request) result {
	conn, err := db.write.Conn(req.ctx)
	if err != nil {
		return result{err: fmt.Errorf("acquire write connection: %w", err)}
	}
	defer conn.Close()
	var ferr error
	res := protect(func() { ferr = req.raw(req.ctx, conn) })
	if res.err == nil && !res.panicked {
		res.err = ferr
	}
	return res
}
