package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// idChunk bounds the number of ids bound to one IN (...) list.
const idChunk = 500

// loadMessage hydrates one message or returns ErrNotFound.
func loadMessage(ctx context.Context, q queryer, id string) (protocol.Message, error) {
	items, err := loadMessageList(ctx, q, []string{id})
	if err != nil {
		return protocol.Message{}, err
	}
	if len(items) == 0 {
		return protocol.Message{}, notFound()
	}
	return items[0], nil
}

// loadMessageList hydrates messages and returns them in the order of ids,
// skipping ids that do not exist.
func loadMessageList(ctx context.Context, q queryer, ids []string) ([]protocol.Message, error) {
	byID := make(map[string]*protocol.Message, len(ids))
	for start := 0; start < len(ids); start += idChunk {
		chunk := ids[start:min(start+idChunk, len(ids))]
		if err := loadMessageChunk(ctx, q, chunk, byID); err != nil {
			return nil, err
		}
	}
	out := make([]protocol.Message, 0, len(ids))
	for _, id := range ids {
		if m, ok := byID[id]; ok {
			out = append(out, *m)
		}
	}
	return out, nil
}

func loadMessageChunk(ctx context.Context, q queryer, ids []string, byID map[string]*protocol.Message) error {
	in := placeholders(len(ids))
	args := anyArgs(ids)

	rows, err := q.QueryContext(ctx, `SELECT m.id, m.channel_id, m.thread_root_id, m.kind, m.body, m.override_name,
			m.mention_channel, m.mention_here, m.reply_count, m.last_reply_at, m.edited_at, m.deleted_at, m.created_at,
			u.id, u.username, u.display_name, u.kind, u.avatar_file_id
		FROM messages m JOIN users u ON u.id = m.author_id WHERE m.id IN (`+in+`)`, args...)
	if err != nil {
		return fmt.Errorf("load messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			m                           protocol.Message
			root, override, avatar      sql.NullString
			mentionChannel, mentionHere int
			lastReply, edited, deleted  sql.NullInt64
			created                     int64
		)
		err := rows.Scan(&m.ID, &m.ChannelID, &root, &m.Kind, &m.Body, &override, &mentionChannel, &mentionHere,
			&m.ReplyCount, &lastReply, &edited, &deleted, &created,
			&m.Author.ID, &m.Author.Username, &m.Author.DisplayName, &m.Author.Kind, &avatar)
		if err != nil {
			return fmt.Errorf("load messages: %w", err)
		}
		if root.Valid {
			m.ThreadRootID = &root.String
		}
		if override.Valid {
			m.OverrideName = &override.String
		}
		if avatar.Valid {
			m.Author.AvatarURL = ptr(protocol.FileURL(avatar.String, "avatar"))
		}
		m.Mentions = protocol.Mentions{UserIDs: []string{}, Channel: mentionChannel == 1, Here: mentionHere == 1}
		m.Files = []protocol.File{}
		m.Reactions = []protocol.Reaction{}
		if lastReply.Valid {
			m.LastReplyAt = ptr(protocol.FormatTime(lastReply.Int64))
		}
		if edited.Valid {
			m.EditedAt = ptr(protocol.FormatTime(edited.Int64))
		}
		m.Deleted = deleted.Valid
		m.CreatedAt = protocol.FormatTime(created)
		byID[m.ID] = &m
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load messages: %w", err)
	}
	rows.Close()

	if err := loadReactions(ctx, q, in, args, byID); err != nil {
		return err
	}
	if err := loadMentionIDs(ctx, q, in, args, byID); err != nil {
		return err
	}
	if err := loadAttachments(ctx, q, in, args, byID); err != nil {
		return err
	}
	return loadAlerts(ctx, q, in, args, byID)
}

func loadReactions(ctx context.Context, q queryer, in string, args []any, byID map[string]*protocol.Message) error {
	rows, err := q.QueryContext(ctx, `SELECT message_id, emoji, user_id FROM reactions WHERE message_id IN (`+in+`)
		ORDER BY message_id, created_at, rowid`, args...)
	if err != nil {
		return fmt.Errorf("load reactions: %w", err)
	}
	defer rows.Close()
	index := map[string]map[string]int{} // message -> emoji -> position
	for rows.Next() {
		var mid, emoji, uid string
		if err := rows.Scan(&mid, &emoji, &uid); err != nil {
			return fmt.Errorf("load reactions: %w", err)
		}
		m := byID[mid]
		if m == nil {
			continue
		}
		if index[mid] == nil {
			index[mid] = map[string]int{}
		}
		pos, ok := index[mid][emoji]
		if !ok {
			pos = len(m.Reactions)
			index[mid][emoji] = pos
			m.Reactions = append(m.Reactions, protocol.Reaction{Emoji: emoji, UserIDs: []string{}})
		}
		r := &m.Reactions[pos]
		r.UserIDs = append(r.UserIDs, uid)
		r.Count++
	}
	return rows.Err()
}

func loadMentionIDs(ctx context.Context, q queryer, in string, args []any, byID map[string]*protocol.Message) error {
	rows, err := q.QueryContext(ctx, `SELECT message_id, user_id FROM mentions WHERE message_id IN (`+in+`) ORDER BY message_id, user_id`, args...)
	if err != nil {
		return fmt.Errorf("load mentions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mid, uid string
		if err := rows.Scan(&mid, &uid); err != nil {
			return fmt.Errorf("load mentions: %w", err)
		}
		if m := byID[mid]; m != nil {
			m.Mentions.UserIDs = append(m.Mentions.UserIDs, uid)
		}
	}
	return rows.Err()
}

func loadAttachments(ctx context.Context, q queryer, in string, args []any, byID map[string]*protocol.Message) error {
	rows, err := q.QueryContext(ctx, `SELECT a.message_id, f.id, f.original_name, f.size, f.mime, f.category,
			f.width, f.height, f.has_thumb, f.meta_stripped
		FROM attachments a JOIN files f ON f.id = a.file_id WHERE a.message_id IN (`+in+`)
		ORDER BY a.message_id, a.position, f.id`, args...)
	if err != nil {
		return fmt.Errorf("load attachments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			mid           string
			f             protocol.File
			width, height sql.NullInt64
			hasThumb      int
			stripped      int
		)
		if err := rows.Scan(&mid, &f.ID, &f.Name, &f.Size, &f.Mime, &f.Category, &width, &height, &hasThumb, &stripped); err != nil {
			return fmt.Errorf("load attachments: %w", err)
		}
		if width.Valid {
			f.Width = ptr(int(width.Int64))
		}
		if height.Valid {
			f.Height = ptr(int(height.Int64))
		}
		f.HasThumb, f.MetaStripped = hasThumb == 1, stripped == 1
		f.URL = protocol.FileURL(f.ID, f.Name)
		if f.HasThumb {
			f.ThumbURL = ptr(protocol.FileThumbURL(f.ID, f.Name))
		}
		if m := byID[mid]; m != nil {
			m.Files = append(m.Files, f)
		}
	}
	return rows.Err()
}

func loadAlerts(ctx context.Context, q queryer, in string, args []any, byID map[string]*protocol.Message) error {
	rows, err := q.QueryContext(ctx, `SELECT message_id, severity, state, title, fields, link, dedupe_key, occurrences,
			first_seen_at, last_seen_at, acked_by, acked_at, resolved_by, resolved_at
		FROM alerts WHERE message_id IN (`+in+`)`, args...)
	if err != nil {
		return fmt.Errorf("load alerts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			mid                       string
			a                         protocol.Alert
			fields                    string
			link, ackedBy, resolvedBy sql.NullString
			first, last               int64
			ackedAt, resolvedAt       sql.NullInt64
		)
		err := rows.Scan(&mid, &a.Severity, &a.State, &a.Title, &fields, &link, &a.DedupeKey, &a.Occurrences,
			&first, &last, &ackedBy, &ackedAt, &resolvedBy, &resolvedAt)
		if err != nil {
			return fmt.Errorf("load alerts: %w", err)
		}
		a.Fields = []protocol.AlertField{}
		if err := json.Unmarshal([]byte(fields), &a.Fields); err != nil {
			return fmt.Errorf("load alerts: fields: %w", err)
		}
		if link.Valid {
			a.Link = &link.String
		}
		a.FirstSeenAt, a.LastSeenAt = protocol.FormatTime(first), protocol.FormatTime(last)
		if ackedBy.Valid {
			a.AckedBy = &ackedBy.String
		}
		a.AckedAt = timePtr(nullInt(ackedAt))
		if resolvedBy.Valid {
			a.ResolvedBy = &resolvedBy.String
		}
		a.ResolvedAt = timePtr(nullInt(resolvedAt))
		if m := byID[mid]; m != nil {
			m.Alert = &a
		}
	}
	return rows.Err()
}

func nullInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}
