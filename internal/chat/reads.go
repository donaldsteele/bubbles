package chat

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// MarkRead moves the actor's read marker in a channel forward to messageID.
// The marker never moves backwards: an older (or equal) id changes nothing
// and emits nothing. It emits read.updated to the actor, carrying the
// resulting unread and mention counts.
func (s *Service) MarkRead(ctx context.Context, actor Actor, channelID, messageID string) (protocol.ReadEvent, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.ReadEvent, error) { return s.MarkReadTx(tx, actor, channelID, messageID) })
}

// MarkReadTx is MarkRead inside the caller's transaction.
func (s *Service) MarkReadTx(tx *store.Tx, actor Actor, channelID, messageID string) (protocol.ReadEvent, error) {
	ctx := tx.Context()
	_, info, err := channelInfoFor(ctx, tx, actor, channelID)
	if err != nil {
		return protocol.ReadEvent{}, err
	}
	if actor.IsApp() && !actor.HasScope(ScopeMessagesRead) {
		return protocol.ReadEvent{}, forbidden("the token lacks the messages:read scope")
	}
	if !info.Member {
		return protocol.ReadEvent{}, forbidden("you are not a member of this channel")
	}
	var inChannel int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id = ? AND channel_id = ?`, messageID, channelID).Scan(&inChannel); err != nil {
		return protocol.ReadEvent{}, fmt.Errorf("check message: %w", err)
	}
	if inChannel == 0 {
		return protocol.ReadEvent{}, invalid("message_id", "unknown message in this channel")
	}
	var current sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT last_read_id FROM memberships WHERE channel_id = ? AND user_id = ?`, channelID, actor.ID).Scan(&current); err != nil {
		return protocol.ReadEvent{}, fmt.Errorf("load read marker: %w", err)
	}
	marker := messageID
	moved := !current.Valid || messageID > current.String
	if moved {
		if _, err := tx.Exec(`UPDATE memberships SET last_read_id = ? WHERE channel_id = ? AND user_id = ?`, messageID, channelID, actor.ID); err != nil {
			return protocol.ReadEvent{}, fmt.Errorf("mark read: %w", err)
		}
	} else {
		marker = current.String
	}
	counts, err := loadUnread(ctx, tx, actor.ID, channelID)
	if err != nil {
		return protocol.ReadEvent{}, err
	}
	ev := protocol.ReadEvent{ChannelID: channelID, LastReadID: marker}
	if len(counts) > 0 {
		ev.Unread, ev.Mentions = counts[0].Unread, counts[0].Mentions
	}
	if moved {
		if err := emit(tx, "read.updated", "", actor.ID, ev); err != nil {
			return protocol.ReadEvent{}, err
		}
	}
	return ev, nil
}

// loadUnread returns the unread and mention counts of the user's channels, or
// of one channel when channelID is set. Unread counts the messages (roots and
// thread replies) after the read marker that the user did not write, that are
// not system messages and not deleted; mentions are those among them that
// mention the user, @channel or @here.
func loadUnread(ctx context.Context, q queryer, userID, channelID string) ([]protocol.Unread, error) {
	query := `SELECT mb.channel_id, COUNT(m.id),
			COALESCE(SUM(CASE WHEN m.mention_channel = 1 OR m.mention_here = 1
				OR EXISTS (SELECT 1 FROM mentions x WHERE x.message_id = m.id AND x.user_id = mb.user_id) THEN 1 ELSE 0 END), 0)
		FROM memberships mb
		LEFT JOIN messages m ON m.channel_id = mb.channel_id AND m.id > COALESCE(mb.last_read_id, '')
			AND m.author_id != mb.user_id AND m.kind != 'system' AND m.deleted_at IS NULL
		WHERE mb.user_id = ?`
	args := []any{userID}
	if channelID != "" {
		query += ` AND mb.channel_id = ?`
		args = append(args, channelID)
	}
	rows, err := q.QueryContext(ctx, query+` GROUP BY mb.channel_id ORDER BY mb.channel_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("count unread: %w", err)
	}
	defer rows.Close()
	out := []protocol.Unread{}
	for rows.Next() {
		var u protocol.Unread
		if err := rows.Scan(&u.ChannelID, &u.Unread, &u.Mentions); err != nil {
			return nil, fmt.Errorf("count unread: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UnreadCounts returns the unread and mention counts of every channel the
// user has joined.
func (s *Service) UnreadCounts(ctx context.Context, userID string) ([]protocol.Unread, error) {
	return loadUnread(ctx, s.db.Read(), userID, "")
}
