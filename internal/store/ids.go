package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// idGen issues ULIDs from monotonic entropy. It is not safe for concurrent
// use: only the writer goroutine calls it, so IDs sort in commit order.
type idGen struct {
	entropy *ulid.MonotonicEntropy
	lastMs  uint64
}

func newIDGen() *idGen {
	return &idGen{entropy: ulid.Monotonic(rand.Reader, 0)}
}

// next returns a ULID strictly greater than every previous one. The
// timestamp never moves backwards (a clock step back reuses the last
// millisecond) and moves forward one millisecond if the random part of the
// current millisecond is exhausted.
func (g *idGen) next(t time.Time) string {
	ms := ulid.Timestamp(t)
	if ms < g.lastMs {
		ms = g.lastMs
	}
	for {
		id, err := ulid.New(ms, g.entropy)
		if err == nil {
			g.lastMs = ms
			return id.String()
		}
		if !errors.Is(err, ulid.ErrMonotonicOverflow) {
			panic(fmt.Sprintf("store: generate ULID: %v", err))
		}
		ms++
	}
}

// seedFromDB makes later IDs sort after every ULID already persisted, so
// monotonicity survives a restart with a clock that moved backwards. It
// reads max(id) of every table with a TEXT id column (all ULID keys: users,
// channels, messages, files, apps, ...) and starts one millisecond past the
// newest timestamp, which also beats any random part issued in that
// millisecond before the restart. Values that are not ULIDs are ignored.
// The caller must own q exclusively (it runs before the writer starts).
func (g *idGen) seedFromDB(ctx context.Context, q *sql.DB) error {
	tables, err := idTables(ctx, q)
	if err != nil {
		return err
	}
	var maxMs uint64
	found := false
	for _, table := range tables {
		ms, ok, err := newestULID(ctx, q, table)
		if err != nil {
			return err
		}
		if ok && (!found || ms > maxMs) {
			maxMs, found = ms, true
		}
	}
	if found && maxMs+1 > g.lastMs {
		g.lastMs = maxMs + 1
	}
	return nil
}

// idTables lists the tables that have a TEXT column named id.
func idTables(ctx context.Context, q *sql.DB) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT m.name FROM sqlite_master m, pragma_table_info(m.name) p
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite\_%' ESCAPE '\' AND p.name = 'id' AND p.type = 'TEXT'
		ORDER BY m.name`)
	if err != nil {
		return nil, fmt.Errorf("seed id generator: list tables: %w", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("seed id generator: list tables: %w", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("seed id generator: list tables: %w", err)
	}
	return tables, nil
}

// newestULID returns the timestamp of the greatest valid ULID in table.id.
func newestULID(ctx context.Context, q *sql.DB, table string) (uint64, bool, error) {
	quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
	rows, err := q.QueryContext(ctx, `SELECT id FROM `+quoted+` WHERE id IS NOT NULL ORDER BY id DESC`)
	if err != nil {
		return 0, false, fmt.Errorf("seed id generator: scan %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return 0, false, fmt.Errorf("seed id generator: scan %s: %w", table, err)
		}
		if id, err := ulid.ParseStrict(s); err == nil {
			return id.Time(), true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return 0, false, fmt.Errorf("seed id generator: scan %s: %w", table, err)
	}
	return 0, false, nil
}
