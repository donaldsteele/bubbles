package events

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/donaldsteele/bubbles/internal/store"
)

// ErrEphemeral is returned (wrapped) by Append for ephemeral event types:
// typing, presence.changed and ephemeral.message are broadcast only.
var ErrEphemeral = errors.New("ephemeral event type is never persisted")

// Publisher receives every appended event after its transaction commits.
// Publish runs on the store writer goroutine, in commit (= seq) order, and
// must never block.
type Publisher interface {
	Publish(e Event)
}

// publisherKey is the store.DB value key of the post-commit Publisher.
type publisherKey struct{}

// SetPublisher makes p receive every event appended through db from now on
// (nil stops publishing). Callers of Append need no changes: Append finds
// the publisher through the transaction and registers an AfterCommit hook,
// so a rolled-back transaction publishes nothing.
func SetPublisher(db *store.DB, p Publisher) { db.SetValue(publisherKey{}, p) }

// Append inserts e into the events table inside tx and returns its seq. An
// empty Payload is stored as {}; a zero CreatedAt takes tx.Now(). Seq on the
// input is ignored. When a Publisher is set on the database, the stored
// event is published to it after the transaction commits.
func Append(tx *store.Tx, e Event) (int64, error) {
	if !Known(e.Type) {
		return 0, fmt.Errorf("append event: unknown type %q", e.Type)
	}
	if IsEphemeral(e.Type) {
		return 0, fmt.Errorf("append event: type %q: %w", e.Type, ErrEphemeral)
	}
	payload := "{}"
	if len(e.Payload) > 0 {
		if !json.Valid(e.Payload) {
			return 0, errors.New("append event: payload is not valid JSON")
		}
		payload = string(e.Payload)
	}
	createdAt := e.CreatedAt
	if createdAt == 0 {
		createdAt = tx.Now()
	}
	res, err := tx.Exec(`INSERT INTO events (type, channel_id, user_id, payload, created_at) VALUES (?, ?, ?, ?, ?)`,
		e.Type, nullString(e.ChannelID), nullString(e.UserID), payload, createdAt)
	if err != nil {
		return 0, fmt.Errorf("append event: %w", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("append event: %w", err)
	}
	if p, ok := tx.Value(publisherKey{}).(Publisher); ok && p != nil {
		stored := Event{
			Seq: seq, Type: e.Type, ChannelID: e.ChannelID, UserID: e.UserID,
			Payload: json.RawMessage(payload), CreatedAt: createdAt,
		}
		tx.AfterCommit(func() { p.Publish(stored) })
	}
	return seq, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
