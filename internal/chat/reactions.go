package chat

import (
	"context"
	"fmt"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// AddReaction adds the actor's emoji reaction to a message and returns the
// message's reactions. Adding a reaction that exists changes nothing. A
// message holds at most 50 distinct emoji. It emits reaction.added.
func (s *Service) AddReaction(ctx context.Context, actor Actor, messageID, emoji string) ([]protocol.Reaction, error) {
	return run(ctx, s, func(tx *store.Tx) ([]protocol.Reaction, error) { return s.AddReactionTx(tx, actor, messageID, emoji) })
}

// AddReactionTx is AddReaction inside the caller's transaction.
func (s *Service) AddReactionTx(tx *store.Tx, actor Actor, messageID, emoji string) ([]protocol.Reaction, error) {
	ctx := tx.Context()
	ref, info, err := refForActor(ctx, tx, actor, messageID)
	if err != nil {
		return nil, err
	}
	if err := Can(actor, ActReact, info); err != nil {
		return nil, err
	}
	if reason := validateEmoji(emoji); reason != "" {
		return nil, invalid("emoji", reason)
	}
	if ref.Deleted {
		return nil, conflict("the message was deleted")
	}
	var mine, anyone, distinct int
	err = tx.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM reactions WHERE message_id = ?1 AND user_id = ?2 AND emoji = ?3),
			(SELECT COUNT(*) FROM reactions WHERE message_id = ?1 AND emoji = ?3),
			(SELECT COUNT(DISTINCT emoji) FROM reactions WHERE message_id = ?1)`,
		ref.ID, actor.ID, emoji).Scan(&mine, &anyone, &distinct)
	if err != nil {
		return nil, fmt.Errorf("check reactions: %w", err)
	}
	if mine == 0 {
		if anyone == 0 && distinct >= MaxReactionsPerMessage {
			return nil, invalid("emoji", fmt.Sprintf("a message holds at most %d different reactions", MaxReactionsPerMessage))
		}
		if _, err := tx.Exec(`INSERT INTO reactions (message_id, user_id, emoji, created_at) VALUES (?, ?, ?, ?)`,
			ref.ID, actor.ID, emoji, tx.Now()); err != nil {
			return nil, fmt.Errorf("add reaction: %w", err)
		}
		err = emit(tx, "reaction.added", ref.ChannelID, "", protocol.ReactionEvent{
			MessageID: ref.ID, ChannelID: ref.ChannelID, UserID: actor.ID, Emoji: emoji, Count: anyone + 1,
		})
		if err != nil {
			return nil, err
		}
	}
	return reactionsOf(ctx, tx, ref.ID)
}

// RemoveReaction removes the actor's emoji reaction and returns the message's
// reactions. Removing a reaction that does not exist changes nothing. It
// emits reaction.removed.
func (s *Service) RemoveReaction(ctx context.Context, actor Actor, messageID, emoji string) ([]protocol.Reaction, error) {
	return run(ctx, s, func(tx *store.Tx) ([]protocol.Reaction, error) {
		return s.RemoveReactionTx(tx, actor, messageID, emoji)
	})
}

// RemoveReactionTx is RemoveReaction inside the caller's transaction.
func (s *Service) RemoveReactionTx(tx *store.Tx, actor Actor, messageID, emoji string) ([]protocol.Reaction, error) {
	ctx := tx.Context()
	ref, info, err := refForActor(ctx, tx, actor, messageID)
	if err != nil {
		return nil, err
	}
	if err := Can(actor, ActReact, info); err != nil {
		return nil, err
	}
	if reason := validateEmoji(emoji); reason != "" {
		return nil, invalid("emoji", reason)
	}
	res, err := tx.Exec(`DELETE FROM reactions WHERE message_id = ? AND user_id = ? AND emoji = ?`, ref.ID, actor.ID, emoji)
	if err != nil {
		return nil, fmt.Errorf("remove reaction: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		var left int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM reactions WHERE message_id = ? AND emoji = ?`, ref.ID, emoji).Scan(&left); err != nil {
			return nil, fmt.Errorf("count reactions: %w", err)
		}
		err = emit(tx, "reaction.removed", ref.ChannelID, "", protocol.ReactionEvent{
			MessageID: ref.ID, ChannelID: ref.ChannelID, UserID: actor.ID, Emoji: emoji, Count: left,
		})
		if err != nil {
			return nil, err
		}
	}
	return reactionsOf(ctx, tx, ref.ID)
}

func reactionsOf(ctx context.Context, q queryer, messageID string) ([]protocol.Reaction, error) {
	m, err := loadMessage(ctx, q, messageID)
	if err != nil {
		return nil, err
	}
	return m.Reactions, nil
}
