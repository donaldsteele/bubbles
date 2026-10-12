package alerts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// rank orders severities: a higher one is an escalation.
func rank(severity string) int {
	switch severity {
	case protocol.SeverityInfo:
		return 1
	case protocol.SeverityWarning:
		return 2
	case protocol.SeverityCritical:
		return 3
	}
	return 0
}

// row is the part of an alerts row (and its message) the service decides on.
type row struct {
	MessageID    string
	ChannelID    string
	AppID        string
	DedupeKey    string
	Severity     string
	State        string
	Occurrences  int
	FirstSeenAt  int64
	LastSeenAt   int64
	LastNotified sql.NullInt64
	RenotifyS    sql.NullInt64
	Deleted      bool
}

const rowSelect = `SELECT a.message_id, m.channel_id, a.app_id, a.dedupe_key, a.severity, a.state, a.occurrences,
		a.first_seen_at, a.last_seen_at, a.last_notified_at, a.renotify_after_s, m.deleted_at IS NOT NULL
	FROM alerts a JOIN messages m ON m.id = a.message_id`

func scanRow(sc interface{ Scan(...any) error }) (row, error) {
	var r row
	err := sc.Scan(&r.MessageID, &r.ChannelID, &r.AppID, &r.DedupeKey, &r.Severity, &r.State, &r.Occurrences,
		&r.FirstSeenAt, &r.LastSeenAt, &r.LastNotified, &r.RenotifyS, &r.Deleted)
	return r, err
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// byID loads the alert of a message, or ErrNotFound.
func byID(ctx context.Context, q queryer, messageID string) (row, error) {
	r, err := scanRow(q.QueryRowContext(ctx, rowSelect+` WHERE a.message_id = ?`, messageID))
	if errors.Is(err, sql.ErrNoRows) {
		return r, notFound()
	}
	if err != nil {
		return r, fmt.Errorf("load alert: %w", err)
	}
	return r, nil
}

// open returns the un-resolved alert of (app, key), if any. An alert whose
// message a moderator deleted is resolved on the spot, so the key can open a
// fresh alert.
func open(tx *store.Tx, appID, key string) (row, bool, error) {
	r, err := scanRow(tx.QueryRowContext(tx.Context(), rowSelect+` WHERE a.app_id = ? AND a.dedupe_key = ? AND a.state != 'resolved'`, appID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("find open alert: %w", err)
	}
	if r.Deleted {
		_, err := tx.Exec(`UPDATE alerts SET state = 'resolved', resolved_at = ? WHERE message_id = ?`, tx.Now(), r.MessageID)
		if err != nil {
			return r, false, fmt.Errorf("resolve deleted alert: %w", err)
		}
		return r, false, nil
	}
	return r, true, nil
}

// latest returns the newest alert of (app, key) whatever its state.
func latest(tx *store.Tx, appID, key string) (row, bool, error) {
	r, err := scanRow(tx.QueryRowContext(tx.Context(), rowSelect+
		` WHERE a.app_id = ? AND a.dedupe_key = ? AND m.deleted_at IS NULL ORDER BY a.message_id DESC LIMIT 1`, appID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("find alert: %w", err)
	}
	return r, true, nil
}

// transition decides what a repeated post did to the alert. A higher
// severity is an escalation. Otherwise, an alert nobody acknowledged whose
// last notification is older than the renotify interval is due again. A
// repeat inside the interval, or of an acknowledged alert, is only an
// update.
func transition(old row, severity string, now int64, defaultRenotifyS int) string {
	if rank(severity) > rank(old.Severity) {
		return protocol.AlertTransitionEscalated
	}
	if old.State == protocol.AlertOpen && renotifyDue(old, now, defaultRenotifyS) {
		return protocol.AlertTransitionRenotifyDue
	}
	return protocol.AlertTransitionUpdatedOther
}

// renotifyDue reports whether the alert's renotify interval has passed since
// it last notified. An interval of 0 turns renotification off.
func renotifyDue(r row, now int64, defaultRenotifyS int) bool {
	every := int64(defaultRenotifyS)
	if r.RenotifyS.Valid {
		every = r.RenotifyS.Int64
	}
	if every <= 0 {
		return false
	}
	if !r.LastNotified.Valid {
		return true
	}
	return now-r.LastNotified.Int64 >= (time.Duration(every) * time.Second).Milliseconds()
}
