package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Bootstrap assembles the GET /bootstrap snapshot for the actor. All reads share
// one read transaction, so the data is consistent with SeqHead: a client that
// replays events after SeqHead converges to the current state. Guests and
// apps see only the users they share a channel with and no channel directory.
// An app token with a channel allowlist also sees only those channels, their
// memberships and unread counts, and the users who share them.
func (s *Service) Bootstrap(ctx context.Context, actor Actor) (protocol.Bootstrap, error) {
	var out protocol.Bootstrap
	tx, err := s.db.Read().BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("bootstrap: %w", err)
	}
	defer tx.Rollback()

	// The sequence head comes first: the snapshot is never older than it.
	err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'events'), 0)`).Scan(&out.SeqHead)
	if err != nil {
		return out, fmt.Errorf("bootstrap: sequence head: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT id, epoch FROM instance`).Scan(&out.InstanceID, &out.Epoch); err != nil {
		return out, fmt.Errorf("bootstrap: instance: %w", err)
	}
	me, err := loadMe(ctx, tx, actor.ID)
	if err != nil {
		return out, err
	}
	out.Me = me
	// The account row decides the role and kind; scopes and the channel
	// allowlist are the token's and come from the caller.
	actor.Role, actor.Kind = me.Role, me.Kind
	if err := requireDirectoryScope(actor); err != nil {
		return out, err
	}
	restricted := actor.Class() == ClassGuest || actor.IsApp()

	if out.Users, err = bootstrapUsers(ctx, tx, actor, restricted); err != nil {
		return out, err
	}
	query := `SELECT ` + channelCols + ` FROM channels c WHERE c.id IN (SELECT channel_id FROM memberships WHERE user_id = ?)`
	if !restricted {
		query += ` OR c.kind = 'public'`
	}
	rows, err := loadChannels(ctx, tx, query+` ORDER BY c.id`, actor.ID)
	if err != nil {
		return out, err
	}
	out.Channels = make([]protocol.Channel, 0, len(rows))
	for _, r := range rows {
		if inScope(actor, r.ID) {
			out.Channels = append(out.Channels, r.wire())
		}
	}
	memberships, err := loadMemberships(ctx, tx, actor.ID)
	if err != nil {
		return out, err
	}
	out.Memberships = make([]protocol.Membership, 0, len(memberships))
	for _, m := range memberships {
		if inScope(actor, m.ChannelID) {
			out.Memberships = append(out.Memberships, m)
		}
	}
	unread, err := loadUnread(ctx, tx, actor.ID, "")
	if err != nil {
		return out, err
	}
	out.Unread = make([]protocol.Unread, 0, len(unread))
	for _, u := range unread {
		if inScope(actor, u.ChannelID) {
			out.Unread = append(out.Unread, u)
		}
	}
	v := s.settings.Values()
	out.SettingsPublic = protocol.SettingsPublic{
		InstanceName: v.InstanceName, UploadMaxBytes: v.UploadMaxBytes, WebPushEnabled: v.WebPushEnabled,
	}
	return out, nil
}

const userCols = `id, username, display_name, kind, role, avatar_file_id, status_text, deactivated_at, created_at`

func scanUser(sc scanner) (protocol.User, error) {
	var (
		u      protocol.User
		avatar sql.NullString
		deact  sql.NullInt64
		born   int64
	)
	if err := sc.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Kind, &u.Role, &avatar, &u.StatusText, &deact, &born); err != nil {
		return u, err
	}
	if avatar.Valid {
		u.AvatarFileID = &avatar.String
	}
	u.Deactivated = deact.Valid
	u.CreatedAt = protocol.FormatTime(born)
	return u, nil
}

func loadMe(ctx context.Context, q queryer, userID string) (protocol.Me, error) {
	var (
		me      protocol.Me
		avatar  sql.NullString
		deact   sql.NullInt64
		born    int64
		prefs   string
		mustChg int
	)
	err := q.QueryRowContext(ctx, `SELECT `+userCols+`, prefs, must_change_pw FROM users WHERE id = ?`, userID).
		Scan(&me.ID, &me.Username, &me.DisplayName, &me.Kind, &me.Role, &avatar, &me.StatusText, &deact, &born, &prefs, &mustChg)
	if errors.Is(err, sql.ErrNoRows) {
		return me, notFound()
	}
	if err != nil {
		return me, fmt.Errorf("bootstrap: load user: %w", err)
	}
	if avatar.Valid {
		me.AvatarFileID = &avatar.String
	}
	me.Deactivated = deact.Valid
	me.CreatedAt = protocol.FormatTime(born)
	me.Prefs = json.RawMessage(prefs)
	me.MustChange = mustChg == 1
	return me, nil
}

// inScope reports whether an app token's channel allowlist admits channelID.
// A nil allowlist admits every channel.
func inScope(a Actor, channelID string) bool {
	return a.ChannelIDs == nil || contains(a.ChannelIDs, channelID)
}

// bootstrapUsers lists the users the caller may know about: everyone, or only
// themselves and the users sharing a channel (direct messages included) when
// restricted. With a channel allowlist, only the channels on it count.
func bootstrapUsers(ctx context.Context, q queryer, actor Actor, restricted bool) ([]protocol.User, error) {
	return visibleUsers(ctx, q, actor, restricted, "", nil)
}

// visibleUsers is bootstrapUsers narrowed by an extra WHERE condition over the
// users table (filter, with its arguments); the visibility rule is the same.
func visibleUsers(ctx context.Context, q queryer, actor Actor, restricted bool, filter string, filterArgs []any) ([]protocol.User, error) {
	query := `SELECT ` + userCols + ` FROM users`
	var conds []string
	var args []any
	if restricted {
		args = []any{actor.ID, actor.ID}
		shared := ""
		if ids := actor.ChannelIDs; ids != nil {
			shared = ` AND 0`
			if len(ids) > 0 {
				shared = ` AND me.channel_id IN (` + placeholders(len(ids)) + `)`
				args = append(args, anyArgs(ids)...)
			}
		}
		conds = append(conds, `(id = ? OR id IN (SELECT t.user_id FROM memberships t
			JOIN memberships me ON me.channel_id = t.channel_id AND me.user_id = ?`+shared+`))`)
	}
	if filter != "" {
		conds = append(conds, filter)
		args = append(args, filterArgs...)
	}
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	out := []protocol.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func loadMemberships(ctx context.Context, q queryer, userID string) ([]protocol.Membership, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+membershipCols+` FROM memberships WHERE user_id = ? ORDER BY channel_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: list memberships: %w", err)
	}
	defer rows.Close()
	out := []protocol.Membership{}
	for rows.Next() {
		m, err := scanMembership(rows)
		if err != nil {
			return nil, fmt.Errorf("bootstrap: list memberships: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
