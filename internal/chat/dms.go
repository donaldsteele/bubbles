package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// DMKey is the dm_key of a set of participants: the sorted, de-duplicated ids
// joined by ",". The same participants give the same key in any order, which
// makes get-or-create idempotent.
func DMKey(ids []string) string {
	u := dedupe(ids)
	slices.Sort(u)
	return strings.Join(u, ",")
}

// StartDM returns the direct message between the actor and userIDs, creating
// it if needed: one other user gives a dm, several a group_dm (at most 9
// participants including the actor), none a note-to-self. The result is the
// same whatever the order of ids; created reports whether a channel was made.
// A new channel emits channel.created.
func (s *Service) StartDM(ctx context.Context, actor Actor, userIDs []string) (protocol.Channel, bool, error) {
	type result struct {
		ch      protocol.Channel
		created bool
	}
	r, err := run(ctx, s, func(tx *store.Tx) (result, error) {
		ch, created, err := s.StartDMTx(tx, actor, userIDs)
		return result{ch, created}, err
	})
	return r.ch, r.created, err
}

// StartDMTx is StartDM inside the caller's transaction.
func (s *Service) StartDMTx(tx *store.Tx, actor Actor, userIDs []string) (protocol.Channel, bool, error) {
	ctx := tx.Context()
	if err := Can(actor, ActStartDM, ChannelInfo{}); err != nil {
		return protocol.Channel{}, false, err
	}
	ids := dedupe(append([]string{actor.ID}, userIDs...))
	if len(ids) > MaxGroupDMParticipants {
		return protocol.Channel{}, false, invalid("user_ids", fmt.Sprintf("a group message has at most %d participants including you", MaxGroupDMParticipants))
	}
	others := ids[1:]
	if err := s.checkDMTargets(ctx, tx, actor, others); err != nil {
		return protocol.Channel{}, false, err
	}

	key := DMKey(ids)
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT id FROM channels WHERE dm_key = ?`, key).Scan(&existing)
	if err == nil {
		row, err := loadChannel(ctx, tx, existing)
		return row.wire(), false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.Channel{}, false, fmt.Errorf("find direct message: %w", err)
	}

	kind := protocol.ChannelDM
	if len(ids) > 2 {
		kind = protocol.ChannelGroupDM
	}
	id := tx.NewID()
	_, err = tx.Exec(`INSERT INTO channels (id, kind, dm_key, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, kind, key, actor.ID, tx.Now())
	if err != nil {
		return protocol.Channel{}, false, fmt.Errorf("create direct message: %w", err)
	}
	memberships := make([]protocol.Membership, 0, len(ids))
	for _, uid := range ids {
		m, err := insertMembership(tx, id, uid, protocol.MemberMember)
		if err != nil {
			return protocol.Channel{}, false, err
		}
		memberships = append(memberships, m)
	}
	row, err := loadChannel(ctx, tx, id)
	if err != nil {
		return protocol.Channel{}, false, err
	}
	ch := row.wire()
	if err := emit(tx, "channel.created", id, "", protocol.ChannelEvent{Channel: ch, Memberships: memberships}); err != nil {
		return protocol.Channel{}, false, err
	}
	return ch, true, nil
}

// checkDMTargets verifies every other participant is an active human the
// actor may message; guests may only message users they share a channel with.
// Unknown, deactivated and invisible users give the same error, so the
// response never reveals which exist.
func (s *Service) checkDMTargets(ctx context.Context, q queryer, actor Actor, others []string) error {
	if len(others) == 0 {
		return nil
	}
	bad := invalid("user_ids", "unknown or unavailable user")
	states, err := loadUserStates(ctx, q, others)
	if err != nil {
		return err
	}
	for _, id := range others {
		if u, ok := states[id]; !ok || u.Deactivated || u.Kind != protocol.KindHuman {
			return bad
		}
	}
	if actor.Class() != ClassGuest {
		return nil
	}
	var shared int
	err = q.QueryRowContext(ctx, `SELECT COUNT(DISTINCT t.user_id) FROM memberships t
		JOIN memberships me ON me.channel_id = t.channel_id AND me.user_id = ?
		WHERE t.user_id IN (`+placeholders(len(others))+`)`, append([]any{actor.ID}, anyArgs(others)...)...).Scan(&shared)
	if err != nil {
		return fmt.Errorf("check shared channels: %w", err)
	}
	if shared != len(others) {
		return bad
	}
	return nil
}
