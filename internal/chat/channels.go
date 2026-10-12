package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Nullable distinguishes "leave unchanged" (Set false), "clear" (Set true,
// Value nil) and "set" (Set true, Value non-nil) in partial updates.
type Nullable[T any] struct {
	Set   bool
	Value *T
}

// Some returns a Nullable that sets v.
func Some[T any](v T) Nullable[T] { return Nullable[T]{Set: true, Value: &v} }

// Clear returns a Nullable that clears the field.
func Clear[T any]() Nullable[T] { return Nullable[T]{Set: true} }

// GeneralName is the name of the default channel created at setup.
const GeneralName = "general"

// CreateChannelParams are the inputs of CreateChannel.
type CreateChannelParams struct {
	Name        string
	Kind        string // public or private
	Topic       string
	Description string
	// MemberIDs are added as plain members next to the creator, who becomes
	// the owner.
	MemberIDs []string
}

// UpdateChannelParams are the partial inputs of UpdateChannel; nil / unset
// fields stay as they are.
type UpdateChannelParams struct {
	Name          *string
	Topic         *string
	Description   *string
	RetentionDays Nullable[int]
}

// CreateChannel creates a public or private channel owned by the actor.
func (s *Service) CreateChannel(ctx context.Context, actor Actor, p CreateChannelParams) (protocol.Channel, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.Channel, error) { return s.CreateChannelTx(tx, actor, p) })
}

// CreateChannelTx is CreateChannel inside the caller's transaction. It emits
// channel.created.
func (s *Service) CreateChannelTx(tx *store.Tx, actor Actor, p CreateChannelParams) (protocol.Channel, error) {
	ctx := tx.Context()
	if err := Can(actor, ActCreateChannel, ChannelInfo{}); err != nil {
		return protocol.Channel{}, err
	}
	if p.Kind != protocol.ChannelPublic && p.Kind != protocol.ChannelPrivate {
		return protocol.Channel{}, invalid("kind", "kind must be public or private")
	}
	name, reason := NormalizeChannelName(p.Name)
	if reason != "" {
		return protocol.Channel{}, invalid("name", reason)
	}
	if err := validateTopicDescription(p.Topic, p.Description); err != nil {
		return protocol.Channel{}, err
	}
	members := make([]string, 0, len(p.MemberIDs))
	for _, id := range dedupe(p.MemberIDs) {
		if id != actor.ID {
			members = append(members, id)
		}
	}
	if len(members) > MaxInitialMembers {
		return protocol.Channel{}, invalid("member_ids", fmt.Sprintf("at most %d members can be added at creation", MaxInitialMembers))
	}
	states, err := loadUserStates(ctx, tx, members)
	if err != nil {
		return protocol.Channel{}, err
	}
	for _, id := range members {
		if u, ok := states[id]; !ok || u.Deactivated {
			return protocol.Channel{}, invalid("member_ids", "unknown or deactivated user")
		}
	}
	return s.createChannel(tx, channelSpec{
		Kind: p.Kind, Name: name, Topic: p.Topic, Description: p.Description,
		CreatedBy: actor.ID, OwnerID: actor.ID, MemberIDs: members,
	})
}

// channelSpec is a fully validated channel to insert.
type channelSpec struct {
	Kind, Name, Topic, Description string
	IsDefault                      bool
	CreatedBy                      string
	OwnerID                        string
	MemberIDs                      []string
}

// createChannel inserts a public or private channel with its memberships and
// emits channel.created. The name must already be normalized.
func (s *Service) createChannel(tx *store.Tx, sp channelSpec) (protocol.Channel, error) {
	ctx := tx.Context()
	var taken int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM channels WHERE name = ? AND kind IN ('public', 'private')`, sp.Name).Scan(&taken)
	if err != nil {
		return protocol.Channel{}, fmt.Errorf("check channel name: %w", err)
	}
	if taken > 0 {
		return protocol.Channel{}, conflict("a channel with this name already exists")
	}
	id := tx.NewID()
	_, err = tx.Exec(`INSERT INTO channels (id, kind, name, topic, description, is_default, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, sp.Kind, sp.Name, sp.Topic, sp.Description, boolInt(sp.IsDefault), nullStr(sp.CreatedBy), tx.Now())
	if err != nil {
		return protocol.Channel{}, fmt.Errorf("create channel: %w", err)
	}
	var memberships []protocol.Membership
	add := func(userID, role string) error {
		m, err := insertMembership(tx, id, userID, role)
		if err != nil {
			return err
		}
		memberships = append(memberships, m)
		return nil
	}
	if sp.OwnerID != "" {
		if err := add(sp.OwnerID, protocol.MemberOwner); err != nil {
			return protocol.Channel{}, err
		}
	}
	for _, uid := range sp.MemberIDs {
		if err := add(uid, protocol.MemberMember); err != nil {
			return protocol.Channel{}, err
		}
	}
	row, err := loadChannel(ctx, tx, id)
	if err != nil {
		return protocol.Channel{}, err
	}
	ch := row.wire()
	if err := emit(tx, "channel.created", id, "", protocol.ChannelEvent{Channel: ch, Memberships: memberships}); err != nil {
		return protocol.Channel{}, err
	}
	return ch, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func validateTopicDescription(topic, description string) error {
	fields := map[string]string{}
	if chars(topic) > MaxTopicChars || !validText(topic) {
		fields["topic"] = "topic must be valid text of at most 250 characters"
	}
	if chars(description) > MaxDescriptionChars || !validText(description) {
		fields["description"] = "description must be valid text of at most 1000 characters"
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// UpdateChannel renames a channel or changes its topic, description or
// retention. Only owners (and admins) may. It emits channel.updated unless
// nothing changed.
func (s *Service) UpdateChannel(ctx context.Context, actor Actor, channelID string, p UpdateChannelParams) (protocol.Channel, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.Channel, error) { return s.UpdateChannelTx(tx, actor, channelID, p) })
}

// UpdateChannelTx is UpdateChannel inside the caller's transaction.
func (s *Service) UpdateChannelTx(tx *store.Tx, actor Actor, channelID string, p UpdateChannelParams) (protocol.Channel, error) {
	ctx := tx.Context()
	row, _, err := authorize(ctx, tx, actor, ActManage, channelID)
	if err != nil {
		return protocol.Channel{}, err
	}
	fields := map[string]string{}
	var sets []string
	var args []any
	if p.Name != nil {
		name, reason := NormalizeChannelName(*p.Name)
		switch {
		case reason != "":
			fields["name"] = reason
		case row.Name == nil || name != *row.Name:
			var taken int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM channels WHERE name = ? AND kind IN ('public', 'private') AND id != ?`,
				name, row.ID).Scan(&taken); err != nil {
				return protocol.Channel{}, fmt.Errorf("check channel name: %w", err)
			}
			if taken > 0 {
				return protocol.Channel{}, conflict("a channel with this name already exists")
			}
			sets, args = append(sets, "name = ?"), append(args, name)
		}
	}
	if p.Topic != nil && *p.Topic != row.Topic {
		if chars(*p.Topic) > MaxTopicChars || !validText(*p.Topic) {
			fields["topic"] = "topic must be valid text of at most 250 characters"
		} else {
			sets, args = append(sets, "topic = ?"), append(args, *p.Topic)
		}
	}
	if p.Description != nil && *p.Description != row.Description {
		if chars(*p.Description) > MaxDescriptionChars || !validText(*p.Description) {
			fields["description"] = "description must be valid text of at most 1000 characters"
		} else {
			sets, args = append(sets, "description = ?"), append(args, *p.Description)
		}
	}
	if p.RetentionDays.Set {
		switch v := p.RetentionDays.Value; {
		case v == nil:
			if row.RetentionDays != nil {
				sets = append(sets, "retention_days = NULL")
			}
		case *v < 1 || *v > settings.MaxRetentionDays:
			fields["retention_days"] = fmt.Sprintf("retention must be between 1 and %d days", settings.MaxRetentionDays)
		default:
			if max := s.settings.DefaultRetentionDays(); max != nil && *v > *max {
				fields["retention_days"] = fmt.Sprintf("retention must not exceed the server limit of %d days", *max)
			} else if row.RetentionDays == nil || *row.RetentionDays != *v {
				sets, args = append(sets, "retention_days = ?"), append(args, *v)
			}
		}
	}
	if len(fields) > 0 {
		return protocol.Channel{}, &ValidationError{Fields: fields}
	}
	if len(sets) == 0 {
		return row.wire(), nil
	}
	return s.updateChannelRow(tx, row.ID, sets, args)
}

// updateChannelRow applies the SET clauses and emits channel.updated.
func (s *Service) updateChannelRow(tx *store.Tx, id string, sets []string, args []any) (protocol.Channel, error) {
	args = append(args, id)
	if _, err := tx.Exec(`UPDATE channels SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return protocol.Channel{}, fmt.Errorf("update channel: %w", err)
	}
	row, err := loadChannel(tx.Context(), tx, id)
	if err != nil {
		return protocol.Channel{}, err
	}
	ch := row.wire()
	if err := emit(tx, "channel.updated", id, "", protocol.ChannelEvent{Channel: ch}); err != nil {
		return protocol.Channel{}, err
	}
	return ch, nil
}

// SetDefault marks a public channel as a default channel (or not). New
// members and admins join default channels automatically. Admin only.
func (s *Service) SetDefault(ctx context.Context, actor Actor, channelID string, isDefault bool) (protocol.Channel, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.Channel, error) { return s.SetDefaultTx(tx, actor, channelID, isDefault) })
}

// SetDefaultTx is SetDefault inside the caller's transaction.
func (s *Service) SetDefaultTx(tx *store.Tx, actor Actor, channelID string, isDefault bool) (protocol.Channel, error) {
	if actor.Class() != ClassAdmin {
		return protocol.Channel{}, forbidden("only an admin can change default channels")
	}
	row, err := loadChannel(tx.Context(), tx, channelID)
	if err != nil {
		return protocol.Channel{}, err
	}
	if row.Kind != protocol.ChannelPublic {
		return protocol.Channel{}, notFoundUnlessMember(tx.Context(), tx, actor, row)
	}
	if row.IsDefault == isDefault {
		return row.wire(), nil
	}
	return s.updateChannelRow(tx, row.ID, []string{"is_default = ?"}, []any{boolInt(isDefault)})
}

// notFoundUnlessMember is the error for an operation that needs a public
// channel on a non-public one: members learn why, everyone else sees nothing.
func notFoundUnlessMember(ctx context.Context, q queryer, actor Actor, row channelRow) error {
	_, member, err := memberRole(ctx, q, row.ID, actor.ID)
	if err != nil {
		return err
	}
	if !member {
		return notFound()
	}
	return invalid("channel_id", "only public channels can be default channels")
}

// ArchiveChannel makes a channel read-only. It emits channel.archived.
func (s *Service) ArchiveChannel(ctx context.Context, actor Actor, channelID string) (protocol.Channel, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.Channel, error) { return s.ArchiveChannelTx(tx, actor, channelID) })
}

// ArchiveChannelTx is ArchiveChannel inside the caller's transaction.
func (s *Service) ArchiveChannelTx(tx *store.Tx, actor Actor, channelID string) (protocol.Channel, error) {
	return s.setArchived(tx, actor, channelID, true)
}

// UnarchiveChannel reopens an archived channel. It emits channel.unarchived.
func (s *Service) UnarchiveChannel(ctx context.Context, actor Actor, channelID string) (protocol.Channel, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.Channel, error) { return s.UnarchiveChannelTx(tx, actor, channelID) })
}

// UnarchiveChannelTx is UnarchiveChannel inside the caller's transaction.
func (s *Service) UnarchiveChannelTx(tx *store.Tx, actor Actor, channelID string) (protocol.Channel, error) {
	return s.setArchived(tx, actor, channelID, false)
}

func (s *Service) setArchived(tx *store.Tx, actor Actor, channelID string, archive bool) (protocol.Channel, error) {
	row, info, err := authorize(tx.Context(), tx, actor, ActArchive, channelID)
	if err != nil {
		return protocol.Channel{}, err
	}
	if archive == info.Archived {
		if archive {
			return protocol.Channel{}, conflict("the channel is already archived")
		}
		return protocol.Channel{}, conflict("the channel is not archived")
	}
	typ := "channel.unarchived"
	var archivedAt any
	if archive {
		typ, archivedAt = "channel.archived", tx.Now()
	}
	if _, err := tx.Exec(`UPDATE channels SET archived_at = ? WHERE id = ?`, archivedAt, row.ID); err != nil {
		return protocol.Channel{}, fmt.Errorf("archive channel: %w", err)
	}
	updated, err := loadChannel(tx.Context(), tx, row.ID)
	if err != nil {
		return protocol.Channel{}, err
	}
	ch := updated.wire()
	if err := emit(tx, typ, row.ID, "", protocol.ChannelEvent{Channel: ch}); err != nil {
		return protocol.Channel{}, err
	}
	return ch, nil
}

// GetChannel returns a channel the actor may view.
func (s *Service) GetChannel(ctx context.Context, actor Actor, channelID string) (protocol.Channel, error) {
	row, _, err := authorize(ctx, s.db.Read(), actor, ActView, channelID)
	if err != nil {
		return protocol.Channel{}, err
	}
	return row.wire(), nil
}

// Channel list scopes.
const (
	ScopeJoined = "joined"
	ScopePublic = "public"
)

// ListChannels returns the channels the actor is in (ScopeJoined) or the
// public directory (ScopePublic, not available to guests and apps).
func (s *Service) ListChannels(ctx context.Context, actor Actor, scope string) ([]protocol.Channel, error) {
	var rows []channelRow
	var err error
	switch scope {
	case ScopeJoined, "":
		if actor.Class() == "" {
			return nil, forbidden("unknown account role")
		}
		if !appCanReadChannels(actor) {
			return nil, forbidden("the token lacks the channels:read scope")
		}
		rows, err = loadChannels(ctx, s.db.Read(), `SELECT `+channelCols+` FROM channels c
			JOIN memberships m ON m.channel_id = c.id WHERE m.user_id = ? ORDER BY c.id`, actor.ID)
	case ScopePublic:
		if class := actor.Class(); class != ClassAdmin && class != ClassMember {
			return nil, forbidden("you cannot browse the channel directory")
		}
		rows, err = loadChannels(ctx, s.db.Read(), `SELECT `+channelCols+` FROM channels c WHERE c.kind = 'public' ORDER BY c.name`)
	default:
		return nil, invalid("scope", "scope must be joined or public")
	}
	if err != nil {
		return nil, err
	}
	out := make([]protocol.Channel, 0, len(rows))
	for _, r := range rows {
		if actor.IsApp() && actor.ChannelIDs != nil && !contains(actor.ChannelIDs, r.ID) {
			continue
		}
		out = append(out, r.wire())
	}
	return out, nil
}

func contains(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// appCanReadChannels reports whether the actor may read channel metadata. An
// app needs channels:read, or messages:read, which already lets it see a
// channel's history (the same pair Can(ActView) accepts).
func appCanReadChannels(a Actor) bool {
	return !a.IsApp() || a.HasScope(ScopeChannelsRead) || a.HasScope(ScopeMessagesRead)
}

// OnSetupTx creates #general, owned by the first admin, inside the setup
// transaction. It matches auth.Options.OnSetup.
func (s *Service) OnSetupTx(_ context.Context, tx *store.Tx, adminID string) error {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM channels WHERE name = ?`, GeneralName).Scan(&n)
	if err != nil {
		return fmt.Errorf("check #general: %w", err)
	}
	if n > 0 {
		return nil
	}
	_, err = s.createChannel(tx, channelSpec{
		Kind: protocol.ChannelPublic, Name: GeneralName, IsDefault: true, CreatedBy: adminID, OwnerID: adminID,
	})
	return err
}

// OnUserCreatedTx joins a new human member or admin to every default channel.
// Guests and apps join nothing; they only see channels they are added to. It
// matches auth.Options.OnUserCreated and emits channel.member_joined per
// channel.
func (s *Service) OnUserCreatedTx(ctx context.Context, tx *store.Tx, userID string) error {
	var role, kind string
	err := tx.QueryRowContext(ctx, `SELECT role, kind FROM users WHERE id = ?`, userID).Scan(&role, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("auto-join: user %s not found", userID)
	}
	if err != nil {
		return fmt.Errorf("auto-join: %w", err)
	}
	if kind != protocol.KindHuman || (role != protocol.RoleAdmin && role != protocol.RoleMember) {
		return nil
	}
	rows, err := loadChannels(ctx, tx, `SELECT `+channelCols+` FROM channels c
		WHERE c.is_default = 1 AND c.archived_at IS NULL ORDER BY c.id`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if _, member, err := memberRole(ctx, tx, r.ID, userID); err != nil {
			return err
		} else if member {
			continue
		}
		if _, err := s.addMember(tx, r.ID, userID, protocol.MemberMember, userID); err != nil {
			return err
		}
	}
	return nil
}
