package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// PinMessage pins a message of the channel; any member may. Pinning a pinned
// message changes nothing. It emits pin.added.
func (s *Service) PinMessage(ctx context.Context, actor Actor, channelID, messageID string) (protocol.Pin, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.Pin, error) { return s.PinMessageTx(tx, actor, channelID, messageID) })
}

// PinMessageTx is PinMessage inside the caller's transaction.
func (s *Service) PinMessageTx(tx *store.Tx, actor Actor, channelID, messageID string) (protocol.Pin, error) {
	ctx := tx.Context()
	_, info, err := authorize(ctx, tx, actor, ActPin, channelID)
	if err != nil {
		return protocol.Pin{}, err
	}
	// The reply carries the pinned message's body, so an app needs messages:read
	// as well as messages:write to pin.
	if err := Can(actor, ActReadContent, info); err != nil {
		return protocol.Pin{}, err
	}
	ref, err := loadRef(ctx, tx, messageID)
	if err != nil {
		return protocol.Pin{}, err
	}
	if ref.ChannelID != channelID {
		return protocol.Pin{}, notFound()
	}
	if ref.Deleted {
		return protocol.Pin{}, conflict("the message was deleted")
	}
	var pinnedBy sql.NullString
	var pinnedAt int64
	err = tx.QueryRowContext(ctx, `SELECT pinned_by, pinned_at FROM pins WHERE channel_id = ? AND message_id = ?`, channelID, messageID).Scan(&pinnedBy, &pinnedAt)
	existing := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return protocol.Pin{}, fmt.Errorf("load pin: %w", err)
	}
	if !existing {
		pinnedBy, pinnedAt = sql.NullString{String: actor.ID, Valid: true}, tx.Now()
		if _, err := tx.Exec(`INSERT INTO pins (channel_id, message_id, pinned_by, pinned_at) VALUES (?, ?, ?, ?)`,
			channelID, messageID, actor.ID, pinnedAt); err != nil {
			return protocol.Pin{}, fmt.Errorf("pin message: %w", err)
		}
		err = emit(tx, "pin.added", channelID, "", protocol.PinEvent{
			ChannelID: channelID, MessageID: messageID, UserID: actor.ID, PinnedAt: protocol.FormatTime(pinnedAt),
		})
		if err != nil {
			return protocol.Pin{}, err
		}
	}
	msg, err := loadMessage(ctx, tx, messageID)
	if err != nil {
		return protocol.Pin{}, err
	}
	pin := protocol.Pin{Message: msg, PinnedAt: protocol.FormatTime(pinnedAt)}
	if pinnedBy.Valid {
		pin.PinnedBy = &pinnedBy.String
	}
	return pin, nil
}

// UnpinMessage removes a pin; any member may. Unpinning a message that is not
// pinned changes nothing. It emits pin.removed.
func (s *Service) UnpinMessage(ctx context.Context, actor Actor, channelID, messageID string) error {
	_, err := run(ctx, s, func(tx *store.Tx) (struct{}, error) {
		return struct{}{}, s.UnpinMessageTx(tx, actor, channelID, messageID)
	})
	return err
}

// UnpinMessageTx is UnpinMessage inside the caller's transaction.
func (s *Service) UnpinMessageTx(tx *store.Tx, actor Actor, channelID, messageID string) error {
	if _, _, err := authorize(tx.Context(), tx, actor, ActPin, channelID); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM pins WHERE channel_id = ? AND message_id = ?`, channelID, messageID)
	if err != nil {
		return fmt.Errorf("unpin message: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	return emit(tx, "pin.removed", channelID, "", protocol.PinEvent{ChannelID: channelID, MessageID: messageID, UserID: actor.ID})
}

// ListPins returns the channel's pinned messages, newest pin first. An app
// needs messages:read.
func (s *Service) ListPins(ctx context.Context, actor Actor, channelID string) ([]protocol.Pin, error) {
	db := s.db.Read()
	if _, _, err := authorize(ctx, db, actor, ActReadContent, channelID); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT message_id, pinned_by, pinned_at FROM pins WHERE channel_id = ? ORDER BY pinned_at DESC, message_id DESC`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list pins: %w", err)
	}
	defer rows.Close()
	var ids []string
	byID := map[string]protocol.Pin{}
	for rows.Next() {
		var (
			id       string
			by       sql.NullString
			pinnedAt int64
		)
		if err := rows.Scan(&id, &by, &pinnedAt); err != nil {
			return nil, fmt.Errorf("list pins: %w", err)
		}
		p := protocol.Pin{PinnedAt: protocol.FormatTime(pinnedAt)}
		if by.Valid {
			p.PinnedBy = &by.String
		}
		ids = append(ids, id)
		byID[id] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pins: %w", err)
	}
	rows.Close()
	msgs, err := loadMessageList(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.Pin, 0, len(msgs))
	for _, m := range msgs {
		p := byID[m.ID]
		p.Message = m
		out = append(out, p)
	}
	return out, nil
}
