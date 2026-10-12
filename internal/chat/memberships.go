package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// MembershipUpdate are the partial inputs of UpdateMembership; unset fields
// stay as they are.
type MembershipUpdate struct {
	NotifyLevel      *string
	AlertMinSeverity *string
	MutedUntil       Nullable[time.Time]
}

// addMember inserts the membership and emits channel.member_joined.
func (s *Service) addMember(tx *store.Tx, channelID, userID, role, actorID string) (protocol.Membership, error) {
	m, err := insertMembership(tx, channelID, userID, role)
	if err != nil {
		return m, err
	}
	row, err := loadChannel(tx.Context(), tx, channelID)
	if err != nil {
		return m, err
	}
	err = emit(tx, "channel.member_joined", channelID, "", protocol.MemberJoinedEvent{
		ChannelID: channelID, UserID: userID, ActorID: actorID, Channel: row.wire(), Membership: m,
	})
	return m, err
}

// removeMember deletes the membership and emits channel.member_left.
func (s *Service) removeMember(tx *store.Tx, channelID, userID, actorID string) error {
	if _, err := tx.Exec(`DELETE FROM memberships WHERE channel_id = ? AND user_id = ?`, channelID, userID); err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	return emit(tx, "channel.member_left", channelID, "", protocol.MemberLeftEvent{ChannelID: channelID, UserID: userID, ActorID: actorID})
}

// JoinChannel joins a public channel. Joining a channel the actor is already
// in changes nothing and emits nothing. It emits channel.member_joined.
func (s *Service) JoinChannel(ctx context.Context, actor Actor, channelID string) (protocol.Membership, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.Membership, error) { return s.JoinChannelTx(tx, actor, channelID) })
}

// JoinChannelTx is JoinChannel inside the caller's transaction.
func (s *Service) JoinChannelTx(tx *store.Tx, actor Actor, channelID string) (protocol.Membership, error) {
	_, info, err := authorize(tx.Context(), tx, actor, ActJoin, channelID)
	if err != nil {
		return protocol.Membership{}, err
	}
	if info.Member {
		m, _, err := loadMembership(tx.Context(), tx, channelID, actor.ID)
		return m, err
	}
	return s.addMember(tx, channelID, actor.ID, protocol.MemberMember, actor.ID)
}

// LeaveChannel removes the actor from a public or private channel. It emits
// channel.member_left.
func (s *Service) LeaveChannel(ctx context.Context, actor Actor, channelID string) error {
	_, err := run(ctx, s, func(tx *store.Tx) (struct{}, error) { return struct{}{}, s.LeaveChannelTx(tx, actor, channelID) })
	return err
}

// LeaveChannelTx is LeaveChannel inside the caller's transaction.
func (s *Service) LeaveChannelTx(tx *store.Tx, actor Actor, channelID string) error {
	if _, _, err := authorize(tx.Context(), tx, actor, ActLeave, channelID); err != nil {
		return err
	}
	return s.removeMember(tx, channelID, actor.ID, actor.ID)
}

// AddMember adds userID to a channel, or changes the role of an existing
// member (role "" means member). Adding an existing member with the same role
// changes nothing. A new member emits channel.member_joined; a role change
// emits membership.updated to that member.
func (s *Service) AddMember(ctx context.Context, actor Actor, channelID, userID, role string) (protocol.Membership, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.Membership, error) {
		return s.AddMemberTx(tx, actor, channelID, userID, role)
	})
}

// AddMemberTx is AddMember inside the caller's transaction.
func (s *Service) AddMemberTx(tx *store.Tx, actor Actor, channelID, userID, role string) (protocol.Membership, error) {
	ctx := tx.Context()
	if _, _, err := authorize(ctx, tx, actor, ActAddMember, channelID); err != nil {
		return protocol.Membership{}, err
	}
	if role == "" {
		role = protocol.MemberMember
	}
	if role != protocol.MemberMember && role != protocol.MemberOwner {
		return protocol.Membership{}, invalid("role", "role must be owner or member")
	}
	states, err := loadUserStates(ctx, tx, []string{userID})
	if err != nil {
		return protocol.Membership{}, err
	}
	target, ok := states[userID]
	if !ok || target.Deactivated {
		return protocol.Membership{}, invalid("user_id", "unknown or deactivated user")
	}
	if target.Kind == protocol.KindApp && actor.Class() != ClassAdmin {
		return protocol.Membership{}, forbidden("only an admin can add an app to a channel")
	}
	if role == protocol.MemberOwner && (target.Role == protocol.RoleGuest || target.Kind == protocol.KindApp) {
		return protocol.Membership{}, invalid("role", "guests and apps cannot own a channel")
	}
	cur, member, err := loadMembership(ctx, tx, channelID, userID)
	if err != nil {
		return protocol.Membership{}, err
	}
	if !member {
		return s.addMember(tx, channelID, userID, role, actor.ID)
	}
	if cur.Role == role {
		return cur, nil
	}
	if _, err := tx.Exec(`UPDATE memberships SET role = ? WHERE channel_id = ? AND user_id = ?`, role, channelID, userID); err != nil {
		return protocol.Membership{}, fmt.Errorf("change member role: %w", err)
	}
	cur.Role = role
	if err := emit(tx, "membership.updated", "", userID, cur); err != nil {
		return protocol.Membership{}, err
	}
	return cur, nil
}

// RemoveMember removes another member from a channel (removing oneself is
// leaving). It emits channel.member_left.
func (s *Service) RemoveMember(ctx context.Context, actor Actor, channelID, userID string) error {
	_, err := run(ctx, s, func(tx *store.Tx) (struct{}, error) {
		return struct{}{}, s.RemoveMemberTx(tx, actor, channelID, userID)
	})
	return err
}

// RemoveMemberTx is RemoveMember inside the caller's transaction.
func (s *Service) RemoveMemberTx(tx *store.Tx, actor Actor, channelID, userID string) error {
	if userID == actor.ID {
		return s.LeaveChannelTx(tx, actor, channelID)
	}
	ctx := tx.Context()
	if _, _, err := authorize(ctx, tx, actor, ActRemoveMember, channelID); err != nil {
		return err
	}
	if _, member, err := memberRole(ctx, tx, channelID, userID); err != nil {
		return err
	} else if !member {
		return notFound()
	}
	return s.removeMember(tx, channelID, userID, actor.ID)
}

// ListMembers returns the members of a channel the actor may view, oldest
// first.
func (s *Service) ListMembers(ctx context.Context, actor Actor, channelID string) ([]protocol.Member, error) {
	db := s.db.Read()
	if _, _, err := authorize(ctx, db, actor, ActView, channelID); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT user_id, role, joined_at FROM memberships WHERE channel_id = ? ORDER BY joined_at, user_id`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()
	out := []protocol.Member{}
	for rows.Next() {
		var m protocol.Member
		var joined int64
		if err := rows.Scan(&m.UserID, &m.Role, &joined); err != nil {
			return nil, fmt.Errorf("list members: %w", err)
		}
		m.JoinedAt = protocol.FormatTime(joined)
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateMembership changes the actor's own notification preferences for a
// channel. It emits membership.updated to the actor unless nothing changed.
func (s *Service) UpdateMembership(ctx context.Context, actor Actor, channelID string, u MembershipUpdate) (protocol.Membership, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.Membership, error) { return s.UpdateMembershipTx(tx, actor, channelID, u) })
}

// UpdateMembershipTx is UpdateMembership inside the caller's transaction.
func (s *Service) UpdateMembershipTx(tx *store.Tx, actor Actor, channelID string, u MembershipUpdate) (protocol.Membership, error) {
	ctx := tx.Context()
	if _, info, err := channelInfoFor(ctx, tx, actor, channelID); err != nil {
		return protocol.Membership{}, err
	} else if !appCanReadChannels(actor) {
		return protocol.Membership{}, forbidden("the token lacks the channels:read scope")
	} else if !info.Member {
		return protocol.Membership{}, forbidden("you are not a member of this channel")
	}
	cur, _, err := loadMembership(ctx, tx, channelID, actor.ID)
	if err != nil {
		return cur, err
	}
	fields := map[string]string{}
	var sets []string
	var args []any
	if u.NotifyLevel != nil {
		switch lvl := *u.NotifyLevel; lvl {
		case protocol.NotifyAll, protocol.NotifyMentions, protocol.NotifyNone:
			if lvl != cur.NotifyLevel {
				sets, args = append(sets, "notify_level = ?"), append(args, lvl)
			}
		default:
			fields["notify_level"] = "notify_level must be all, mentions or none"
		}
	}
	if u.AlertMinSeverity != nil {
		switch sev := *u.AlertMinSeverity; sev {
		case protocol.SeverityInfo, protocol.SeverityWarning, protocol.SeverityCritical:
			if sev != cur.AlertMinSeverity {
				sets, args = append(sets, "alert_min_severity = ?"), append(args, sev)
			}
		default:
			fields["alert_min_severity"] = "alert_min_severity must be info, warning or critical"
		}
	}
	if u.MutedUntil.Set {
		if t := u.MutedUntil.Value; t == nil {
			if cur.MutedUntil != nil {
				sets = append(sets, "muted_until = NULL")
			}
		} else if ms := t.UnixMilli(); cur.MutedUntil == nil || *cur.MutedUntil != protocol.FormatTime(ms) {
			sets, args = append(sets, "muted_until = ?"), append(args, ms)
		}
	}
	if len(fields) > 0 {
		return cur, &ValidationError{Fields: fields}
	}
	if len(sets) == 0 {
		return cur, nil
	}
	args = append(args, channelID, actor.ID)
	if _, err := tx.Exec(`UPDATE memberships SET `+strings.Join(sets, ", ")+` WHERE channel_id = ? AND user_id = ?`, args...); err != nil {
		return cur, fmt.Errorf("update membership: %w", err)
	}
	m, _, err := loadMembership(ctx, tx, channelID, actor.ID)
	if err != nil {
		return m, err
	}
	if err := emit(tx, "membership.updated", "", actor.ID, m); err != nil {
		return m, err
	}
	return m, nil
}

// channelInfoFor is channelInfo plus the visibility check of ActView, so a
// channel the actor cannot see is ErrNotFound.
func channelInfoFor(ctx context.Context, q queryer, actor Actor, channelID string) (channelRow, ChannelInfo, error) {
	row, info, err := channelInfo(ctx, q, actor, channelID)
	if err != nil {
		return row, info, err
	}
	if !visible(actor, actor.Class(), info) {
		return row, info, notFound()
	}
	return row, info, nil
}
