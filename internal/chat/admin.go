package chat

import (
	"context"
	"fmt"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// AdminListChannels returns every public and private channel with its name,
// settings and member count, whether or not the admin is a member. It never
// reads messages; DMs and group DMs are not listed.
func (s *Service) AdminListChannels(ctx context.Context, actor Actor) ([]protocol.Channel, error) {
	if actor.Class() != ClassAdmin || actor.IsApp() {
		return nil, forbidden("only an admin can list all channels")
	}
	rows, err := loadChannels(ctx, s.db.Read(), `SELECT `+channelCols+` FROM channels c
		WHERE c.kind IN ('public', 'private') ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.Channel, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.wire())
	}
	return out, nil
}

// AdminChannelPatch is a partial admin change of a public or private channel.
type AdminChannelPatch struct {
	RetentionDays Nullable[int]
	IsDefault     *bool
	Archived      *bool
}

// AdminUpdateChannel sets retention, the default flag and the archived state
// of a public or private channel without needing to be a member. Events are
// the usual channel.updated / channel.archived / channel.unarchived.
func (s *Service) AdminUpdateChannel(ctx context.Context, actor audit.Actor, admin Actor, channelID string, p AdminChannelPatch) (protocol.Channel, error) {
	if admin.Class() != ClassAdmin || admin.IsApp() {
		return protocol.Channel{}, forbidden("only an admin can change channels this way")
	}
	return run(ctx, s, func(tx *store.Tx) (protocol.Channel, error) {
		row, err := loadChannel(ctx, tx, channelID)
		if err != nil {
			return protocol.Channel{}, err
		}
		if row.Kind != protocol.ChannelPublic && row.Kind != protocol.ChannelPrivate {
			return protocol.Channel{}, notFound()
		}
		fields := map[string]string{}
		var sets []string
		var args []any
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
		if p.IsDefault != nil && *p.IsDefault != row.IsDefault {
			if row.Kind != protocol.ChannelPublic {
				fields["is_default"] = "only public channels can be default channels"
			} else {
				sets, args = append(sets, "is_default = ?"), append(args, boolInt(*p.IsDefault))
			}
		}
		if len(fields) > 0 {
			return protocol.Channel{}, &ValidationError{Fields: fields}
		}
		ch := row.wire()
		if len(sets) > 0 {
			if ch, err = s.updateChannelRow(tx, row.ID, sets, args); err != nil {
				return protocol.Channel{}, err
			}
		}
		if p.Archived != nil && *p.Archived != (row.ArchivedAt != nil) {
			typ := "channel.unarchived"
			var archivedAt any
			if *p.Archived {
				typ, archivedAt = "channel.archived", tx.Now()
			}
			if _, err := tx.Exec(`UPDATE channels SET archived_at = ? WHERE id = ?`, archivedAt, row.ID); err != nil {
				return protocol.Channel{}, fmt.Errorf("archive channel: %w", err)
			}
			updated, err := loadChannel(ctx, tx, row.ID)
			if err != nil {
				return protocol.Channel{}, err
			}
			ch = updated.wire()
			if err := emit(tx, typ, row.ID, "", protocol.ChannelEvent{Channel: ch}); err != nil {
				return protocol.Channel{}, err
			}
		}
		if _, err := audit.Write(tx, actor.Entry(audit.ActionChannelAdmin, "channel", row.ID, map[string]any{
			"retention_days": ch.RetentionDays, "is_default": ch.IsDefault, "archived": ch.ArchivedAt != nil,
		})); err != nil {
			return protocol.Channel{}, err
		}
		return ch, nil
	})
}
