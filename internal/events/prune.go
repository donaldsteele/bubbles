package events

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/donaldsteele/bubbles/internal/store"
)

// Retention of the event log: older events are pruned and a resume point
// before the oldest retained event gets resync_required.
const (
	Retention     = 7 * 24 * time.Hour
	PruneInterval = time.Hour
	pruneBatch    = 500
)

// Prune deletes the events created before now - Retention, oldest first,
// in transactions of at most 500 rows, and returns how many it deleted. It
// always removes a prefix of the log (every seq up to the newest expired
// one), so the retained events stay contiguous even if the clock stepped
// backwards. Nothing is published.
func Prune(ctx context.Context, db *store.DB, now time.Time) (int64, error) {
	cutoff := now.Add(-Retention).UnixMilli()
	var upTo sql.NullInt64
	if err := db.Read().QueryRowContext(ctx, `SELECT MAX(seq) FROM events WHERE created_at < ?`, cutoff).Scan(&upTo); err != nil {
		return 0, fmt.Errorf("prune events: %w", err)
	}
	if !upTo.Valid {
		return 0, nil
	}
	var total int64
	for {
		var n int64
		err := db.Do(ctx, func(tx *store.Tx) error {
			res, err := tx.Exec(`DELETE FROM events WHERE seq IN (SELECT seq FROM events WHERE seq <= ? ORDER BY seq LIMIT ?)`,
				upTo.Int64, pruneBatch)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			return err
		})
		if err != nil {
			return total, fmt.Errorf("prune events: %w", err)
		}
		total += n
		if n < pruneBatch {
			return total, nil
		}
	}
}

// Querier is the read side shared by *sql.DB and *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Bounds returns the oldest retained seq (0 when the log is empty) and the
// sequence head: the highest seq ever assigned, which survives pruning and
// matches the bootstrap snapshot's seq_head.
func Bounds(ctx context.Context, q Querier) (oldest, head int64, err error) {
	err = q.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT MIN(seq) FROM events), 0),
		COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'events'), 0)`).Scan(&oldest, &head)
	if err != nil {
		return 0, 0, fmt.Errorf("event log bounds: %w", err)
	}
	return oldest, head, nil
}

// Count returns how many retained events have after < seq <= upTo. Seqs are
// unique and only the oldest prefix is ever pruned, so the range is fully
// retained exactly when Count equals upTo-after.
func Count(ctx context.Context, q Querier, after, upTo int64) (int64, error) {
	var n int64
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE seq > ? AND seq <= ?`, after, upTo).Scan(&n); err != nil {
		return 0, fmt.Errorf("count events: %w", err)
	}
	return n, nil
}

// Range returns up to limit retained events with after < seq <= upTo, in
// seq order.
func Range(ctx context.Context, q Querier, after, upTo int64, limit int) ([]Event, error) {
	rows, err := q.QueryContext(ctx, `SELECT seq, type, COALESCE(channel_id, ''), COALESCE(user_id, ''), payload, created_at
		FROM events WHERE seq > ? AND seq <= ? ORDER BY seq LIMIT ?`, after, upTo, limit)
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var payload string
		if err := rows.Scan(&e.Seq, &e.Type, &e.ChannelID, &e.UserID, &payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("read events: %w", err)
		}
		e.Payload = []byte(payload)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	return out, nil
}
