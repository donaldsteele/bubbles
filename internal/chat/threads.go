package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// resolveThreadRoot returns the id of the thread a reply to messageID belongs
// to: the message itself when it is a root, else its root. The message must
// exist in the channel.
func resolveThreadRoot(ctx context.Context, q queryer, channelID, messageID string) (string, error) {
	var (
		channel string
		root    sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT channel_id, thread_root_id FROM messages WHERE id = ?`, messageID).Scan(&channel, &root)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && channel != channelID) {
		return "", invalid("thread_root_id", "unknown message in this channel")
	}
	if err != nil {
		return "", fmt.Errorf("load thread root: %w", err)
	}
	if root.Valid {
		return root.String, nil
	}
	return messageID, nil
}

// refreshThread recomputes the reply count and last reply time of a root
// after one of its replies was deleted, and emits message.updated for it.
func (s *Service) refreshThread(tx *store.Tx, channelID, rootID string) error {
	_, err := tx.Exec(`UPDATE messages SET
			reply_count = (SELECT COUNT(*) FROM messages r WHERE r.thread_root_id = messages.id AND r.deleted_at IS NULL),
			last_reply_at = (SELECT MAX(r.created_at) FROM messages r WHERE r.thread_root_id = messages.id AND r.deleted_at IS NULL)
		WHERE id = ?`, rootID)
	if err != nil {
		return fmt.Errorf("update thread: %w", err)
	}
	root, err := loadMessage(tx.Context(), tx, rootID)
	if err != nil {
		return err
	}
	return emit(tx, "message.updated", channelID, "", root)
}

// ListThread returns the thread messageID belongs to: its root and a page of
// the replies, oldest first. messageID may be the root or any reply. An app
// needs messages:read.
func (s *Service) ListThread(ctx context.Context, actor Actor, messageID string, o PageOptions) (protocol.Thread, error) {
	db := s.db.Read()
	ref, err := contentRefForActor(ctx, db, actor, messageID)
	if err != nil {
		return protocol.Thread{}, err
	}
	rootID := ref.ID
	if ref.RootID != nil {
		rootID = *ref.RootID
	}
	page, err := pageMessages(ctx, db, `thread_root_id = ?`, []any{rootID}, o)
	if err != nil {
		return protocol.Thread{}, err
	}
	root, err := loadMessage(ctx, db, rootID)
	if err != nil {
		return protocol.Thread{}, err
	}
	return protocol.Thread{Root: root, Replies: page.Items, HasMoreBefore: page.HasMoreBefore, HasMoreAfter: page.HasMoreAfter}, nil
}
