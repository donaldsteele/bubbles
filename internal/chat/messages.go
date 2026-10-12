package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// CreateMessageParams are the inputs of CreateMessage and CreateMessageTx.
type CreateMessageParams struct {
	ChannelID string
	// AuthorID is the sender. CreateMessage overwrites it with the actor.
	AuthorID string
	// Kind is text (the default), alert or system. Alert and system messages
	// never carry mentions, whatever their body says.
	Kind string
	Body string
	// ThreadRootID makes the message a reply. Replying to a reply attaches
	// to the reply's thread root.
	ThreadRootID string
	FileIDs      []string
	// Nonce makes the call idempotent per author: the same nonce returns the
	// existing message and writes nothing.
	Nonce string
	// OverrideName is the display name of a webhook post; it can only be set
	// through CreateMessageTx.
	OverrideName string
	// DisableMentions stores the message without mention rows and with
	// mention_channel and mention_here at 0, whatever the body says. Webhook
	// posts set it: their text comes from outside, so it must not be able to
	// notify anyone.
	DisableMentions bool
	// AfterInsert runs inside the transaction once the message row exists
	// and before the message.created event is built, so the alerts service
	// can insert its alerts row and have it in the event payload.
	AfterInsert func(tx *store.Tx, messageID string) error
}

// PageOptions select a page of messages by id. At most one of Before, After
// and Around may be set; none means the newest messages.
type PageOptions struct {
	Before, After, Around string
	Limit                 int
}

// CreateMessage posts a text message as actor and reports whether it was
// created (false: the nonce matched an existing message and nothing was
// written). It emits message.created, and message.updated for the thread root
// of a reply.
func (s *Service) CreateMessage(ctx context.Context, actor Actor, p CreateMessageParams) (protocol.Message, bool, error) {
	type result struct {
		msg     protocol.Message
		created bool
	}
	r, err := run(ctx, s, func(tx *store.Tx) (result, error) {
		msg, created, err := s.CreateMessageAsTx(tx, actor, p)
		return result{msg, created}, err
	})
	return r.msg, r.created, err
}

// CreateMessageAsTx is CreateMessage inside the caller's transaction.
func (s *Service) CreateMessageAsTx(tx *store.Tx, actor Actor, p CreateMessageParams) (protocol.Message, bool, error) {
	ctx := tx.Context()
	p.AuthorID = actor.ID
	if p.Kind != "" && p.Kind != protocol.MessageText {
		return protocol.Message{}, false, invalid("kind", "only text messages can be posted")
	}
	if p.OverrideName != "" {
		return protocol.Message{}, false, invalid("override_name", "not allowed")
	}
	_, info, err := channelInfo(ctx, tx, actor, p.ChannelID)
	if err != nil {
		return protocol.Message{}, false, err
	}
	if !visible(actor, actor.Class(), info) {
		return protocol.Message{}, false, notFound()
	}
	// A retry of an accepted post is answered with the stored message even if
	// the channel was archived in between.
	if msg, ok, err := replayNonce(ctx, tx, p); err != nil || ok {
		return msg, false, err
	}
	if err := Can(actor, ActPost, info); err != nil {
		return protocol.Message{}, false, err
	}
	return s.createMessage(tx, p)
}

// CreateMessageTx writes a message and its events inside the caller's
// transaction without checking who may post: the caller (alerts, webhooks,
// commands, system notices) has authorized the author already. The channel
// must exist and not be archived; the author need not be a member.
func (s *Service) CreateMessageTx(tx *store.Tx, p CreateMessageParams) (protocol.Message, error) {
	msg, _, err := s.createMessage(tx, p)
	return msg, err
}

func (s *Service) createMessage(tx *store.Tx, p CreateMessageParams) (protocol.Message, bool, error) {
	ctx := tx.Context()
	ch, err := loadChannel(ctx, tx, p.ChannelID)
	if err != nil {
		return protocol.Message{}, false, err
	}
	if msg, ok, err := replayNonce(ctx, tx, p); err != nil || ok {
		return msg, false, err
	}
	if ch.ArchivedAt != nil {
		return protocol.Message{}, false, conflict("the channel is archived")
	}
	kind := p.Kind
	if kind == "" {
		kind = protocol.MessageText
	}
	if kind != protocol.MessageText && kind != protocol.MessageAlert && kind != protocol.MessageSystem {
		return protocol.Message{}, false, invalid("kind", "unknown message kind")
	}
	body, err := validateBody(kind, p.Body, len(p.FileIDs))
	if err != nil {
		return protocol.Message{}, false, err
	}
	if err := validateMessageExtras(p); err != nil {
		return protocol.Message{}, false, err
	}
	attacher := s.fileAttacher()
	if len(p.FileIDs) > 0 && attacher == nil {
		return protocol.Message{}, false, invalid("file_ids", "file attachments are not available")
	}
	author, err := loadUserStates(ctx, tx, []string{p.AuthorID})
	if err != nil {
		return protocol.Message{}, false, err
	}
	if _, ok := author[p.AuthorID]; !ok {
		return protocol.Message{}, false, invalid("author_id", "unknown author")
	}
	rootID := ""
	if p.ThreadRootID != "" {
		if rootID, err = resolveThreadRoot(ctx, tx, p.ChannelID, p.ThreadRootID); err != nil {
			return protocol.Message{}, false, err
		}
	}

	parsed := parseBody(body)
	if p.DisableMentions || kind != protocol.MessageText {
		parsed.Mentions = Mentions{}
	}
	mentioned, err := resolveMentions(ctx, tx, p.ChannelID, p.AuthorID, parsed.Mentions.Usernames)
	if err != nil {
		return protocol.Message{}, false, err
	}
	id, now := tx.NewID(), tx.Now()
	_, err = tx.Exec(`INSERT INTO messages (id, channel_id, author_id, thread_root_id, kind, body, body_plain, override_name,
			mention_channel, mention_here, client_nonce, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, p.ChannelID, p.AuthorID, nullStr(rootID), kind, body, parsed.Plain, nullStr(p.OverrideName),
		boolInt(parsed.Mentions.Channel), boolInt(parsed.Mentions.Here), nullStr(p.Nonce), now)
	if err != nil {
		return protocol.Message{}, false, fmt.Errorf("insert message: %w", err)
	}
	if err := insertMentions(tx, id, mentioned); err != nil {
		return protocol.Message{}, false, err
	}
	var files []protocol.File
	if len(p.FileIDs) > 0 {
		if files, err = attacher.AttachTx(tx, id, p.AuthorID, p.FileIDs); err != nil {
			return protocol.Message{}, false, err
		}
	}
	if rootID != "" {
		if _, err := tx.Exec(`UPDATE messages SET reply_count = reply_count + 1, last_reply_at = ? WHERE id = ?`, now, rootID); err != nil {
			return protocol.Message{}, false, fmt.Errorf("update thread: %w", err)
		}
	}
	if p.AfterInsert != nil {
		if err := p.AfterInsert(tx, id); err != nil {
			return protocol.Message{}, false, err
		}
	}
	msg, err := loadMessage(ctx, tx, id)
	if err != nil {
		return protocol.Message{}, false, err
	}
	if len(p.FileIDs) > 0 {
		msg.Files = files
	}
	if err := emit(tx, "message.created", p.ChannelID, "", msg); err != nil {
		return protocol.Message{}, false, err
	}
	if rootID != "" {
		root, err := loadMessage(ctx, tx, rootID)
		if err != nil {
			return protocol.Message{}, false, err
		}
		if err := emit(tx, "message.updated", p.ChannelID, "", root); err != nil {
			return protocol.Message{}, false, err
		}
	}
	return msg, true, nil
}

// validateBody trims body and checks its limits. A text message needs a body
// unless it carries files.
func validateBody(kind, body string, files int) (string, error) {
	body = cleanBody(body)
	switch {
	case !validText(body):
		return "", invalid("body", "body must be valid text")
	case chars(body) > MaxBodyChars:
		return "", invalid("body", fmt.Sprintf("body must be at most %d characters", MaxBodyChars))
	case kind == protocol.MessageText && strings.TrimSpace(body) == "" && files == 0:
		return "", invalid("body", "body must not be empty")
	}
	return body, nil
}

func validateMessageExtras(p CreateMessageParams) error {
	fields := map[string]string{}
	if len(p.FileIDs) > MaxFileIDs {
		fields["file_ids"] = fmt.Sprintf("at most %d files per message", MaxFileIDs)
	} else if len(dedupe(p.FileIDs)) != len(p.FileIDs) {
		fields["file_ids"] = "file_ids must be unique"
	}
	if len(p.Nonce) > MaxNonceLen || !validText(p.Nonce) {
		fields["nonce"] = fmt.Sprintf("nonce must be at most %d characters", MaxNonceLen)
	}
	if chars(p.OverrideName) > MaxOverrideNameChars || !validText(p.OverrideName) {
		fields["override_name"] = fmt.Sprintf("override_name must be at most %d characters", MaxOverrideNameChars)
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// nonceHit is the stored message an author's nonce was used for.
type nonceHit struct {
	ID        string
	ChannelID string
	RootID    string
}

// messageByNonce finds the author's message with this nonce.
func messageByNonce(ctx context.Context, q queryer, authorID, nonce string) (nonceHit, bool, error) {
	var h nonceHit
	if nonce == "" {
		return h, false, nil
	}
	var root sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id, channel_id, thread_root_id FROM messages WHERE author_id = ? AND client_nonce = ?`,
		authorID, nonce).Scan(&h.ID, &h.ChannelID, &root)
	if errors.Is(err, sql.ErrNoRows) {
		return h, false, nil
	}
	if err != nil {
		return h, false, fmt.Errorf("find message by nonce: %w", err)
	}
	h.RootID = root.String
	return h, true, nil
}

// replayNonce answers a retry of an accepted post with the stored message. The
// caller has authorized p.ChannelID already. A nonce used for a message in
// another channel, or in another thread of this channel, is a validation error
// on "nonce" and nothing of that message is loaded or returned.
func replayNonce(ctx context.Context, tx *store.Tx, p CreateMessageParams) (protocol.Message, bool, error) {
	hit, ok, err := messageByNonce(ctx, tx, p.AuthorID, p.Nonce)
	if err != nil || !ok {
		return protocol.Message{}, false, err
	}
	if hit.ChannelID != p.ChannelID {
		return protocol.Message{}, false, invalid("nonce", "nonce was already used in another channel")
	}
	wantRoot := ""
	if p.ThreadRootID != "" {
		if wantRoot, err = resolveThreadRoot(ctx, tx, p.ChannelID, p.ThreadRootID); err != nil {
			return protocol.Message{}, false, err
		}
	}
	if hit.RootID != wantRoot {
		return protocol.Message{}, false, invalid("nonce", "nonce was already used in another thread")
	}
	msg, err := loadMessage(ctx, tx, hit.ID)
	if err != nil {
		return protocol.Message{}, false, err
	}
	return msg, true, nil
}

// resolveMentions returns the ids of the users named by usernames who are
// active members of the channel, excluding the author.
func resolveMentions(ctx context.Context, q queryer, channelID, authorID string, usernames []string) ([]string, error) {
	if len(usernames) == 0 {
		return nil, nil
	}
	args := append([]any{channelID, authorID}, anyArgs(usernames)...)
	rows, err := q.QueryContext(ctx, `SELECT u.id FROM users u
		JOIN memberships mb ON mb.user_id = u.id AND mb.channel_id = ?
		WHERE u.id != ? AND u.deactivated_at IS NULL AND u.username IN (`+placeholders(len(usernames))+`) ORDER BY u.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("resolve mentions: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("resolve mentions: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func insertMentions(tx *store.Tx, messageID string, userIDs []string) error {
	for _, uid := range userIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO mentions (message_id, user_id) VALUES (?, ?)`, messageID, uid); err != nil {
			return fmt.Errorf("insert mention: %w", err)
		}
	}
	return nil
}

// msgRef is the part of a message the permission checks need.
type msgRef struct {
	ID        string
	ChannelID string
	AuthorID  string
	Kind      string
	Body      string
	RootID    *string
	Deleted   bool
}

func loadRef(ctx context.Context, q queryer, id string) (msgRef, error) {
	var (
		r       msgRef
		root    sql.NullString
		deleted sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `SELECT id, channel_id, author_id, kind, body, thread_root_id, deleted_at FROM messages WHERE id = ?`, id).
		Scan(&r.ID, &r.ChannelID, &r.AuthorID, &r.Kind, &r.Body, &root, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return r, notFound()
	}
	if err != nil {
		return r, fmt.Errorf("load message: %w", err)
	}
	if root.Valid {
		r.RootID = &root.String
	}
	r.Deleted = deleted.Valid
	return r, nil
}

// refForActor loads a message and the actor's relation to its channel; a
// message in a channel the actor cannot see is ErrNotFound.
func refForActor(ctx context.Context, q queryer, actor Actor, messageID string) (msgRef, ChannelInfo, error) {
	ref, err := loadRef(ctx, q, messageID)
	if err != nil {
		return ref, ChannelInfo{}, err
	}
	_, info, err := channelInfo(ctx, q, actor, ref.ChannelID)
	if err != nil {
		return ref, info, err
	}
	if !visible(actor, actor.Class(), info) {
		return ref, info, notFound()
	}
	return ref, info, nil
}

// contentRefForActor is refForActor for a read that returns the message body:
// the actor must also hold the content right (ActReadContent), so a
// channels:read-only app gets ErrForbidden on a channel it can see.
func contentRefForActor(ctx context.Context, q queryer, actor Actor, messageID string) (msgRef, error) {
	ref, info, err := refForActor(ctx, q, actor, messageID)
	if err != nil {
		return ref, err
	}
	return ref, Can(actor, ActReadContent, info)
}

// EditMessage replaces the body of the actor's own text message. It emits
// message.updated and re-extracts the mentions. Editing a deleted message is
// ErrConflict.
func (s *Service) EditMessage(ctx context.Context, actor Actor, messageID, body string) (protocol.Message, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.Message, error) { return s.EditMessageTx(tx, actor, messageID, body) })
}

// EditMessageTx is EditMessage inside the caller's transaction.
func (s *Service) EditMessageTx(tx *store.Tx, actor Actor, messageID, body string) (protocol.Message, error) {
	ctx := tx.Context()
	ref, info, err := refForActor(ctx, tx, actor, messageID)
	if err != nil {
		return protocol.Message{}, err
	}
	switch {
	case ref.AuthorID != actor.ID:
		return protocol.Message{}, forbidden("you can only edit your own messages")
	case ref.Kind != protocol.MessageText:
		return protocol.Message{}, forbidden("only text messages can be edited")
	case ref.Deleted:
		return protocol.Message{}, conflict("the message was deleted")
	}
	if err := Can(actor, ActPost, info); err != nil {
		return protocol.Message{}, err
	}
	var files int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attachments WHERE message_id = ?`, ref.ID).Scan(&files); err != nil {
		return protocol.Message{}, fmt.Errorf("count attachments: %w", err)
	}
	clean, err := validateBody(ref.Kind, body, files)
	if err != nil {
		return protocol.Message{}, err
	}
	if clean == ref.Body {
		return loadMessage(ctx, tx, ref.ID)
	}
	parsed := parseBody(clean)
	mentioned, err := resolveMentions(ctx, tx, ref.ChannelID, ref.AuthorID, parsed.Mentions.Usernames)
	if err != nil {
		return protocol.Message{}, err
	}
	_, err = tx.Exec(`UPDATE messages SET body = ?, body_plain = ?, mention_channel = ?, mention_here = ?, edited_at = ? WHERE id = ?`,
		clean, parsed.Plain, boolInt(parsed.Mentions.Channel), boolInt(parsed.Mentions.Here), tx.Now(), ref.ID)
	if err != nil {
		return protocol.Message{}, fmt.Errorf("edit message: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM mentions WHERE message_id = ?`, ref.ID); err != nil {
		return protocol.Message{}, fmt.Errorf("edit message: %w", err)
	}
	if err := insertMentions(tx, ref.ID, mentioned); err != nil {
		return protocol.Message{}, err
	}
	msg, err := loadMessage(ctx, tx, ref.ID)
	if err != nil {
		return protocol.Message{}, err
	}
	if err := emit(tx, "message.updated", ref.ChannelID, "", msg); err != nil {
		return protocol.Message{}, err
	}
	return msg, nil
}

// DeleteMessage turns a message into a tombstone: the body, reactions,
// mentions, pin and attachments are removed, the row stays so threads and ids
// remain stable. The author, a channel owner or an admin (see ActDeleteAny)
// may delete. It emits message.deleted, pin.removed for a pinned message and
// message.updated for the root of a deleted reply. Deleting a deleted message
// does nothing.
func (s *Service) DeleteMessage(ctx context.Context, actor Actor, messageID string) error {
	_, err := run(ctx, s, func(tx *store.Tx) (struct{}, error) { return struct{}{}, s.DeleteMessageTx(tx, actor, messageID) })
	return err
}

// DeleteMessageTx is DeleteMessage inside the caller's transaction.
func (s *Service) DeleteMessageTx(tx *store.Tx, actor Actor, messageID string) error {
	ctx := tx.Context()
	ref, info, err := refForActor(ctx, tx, actor, messageID)
	if err != nil {
		return err
	}
	if ref.AuthorID == actor.ID {
		switch {
		case actor.IsApp() && !actor.HasScope(ScopeMessagesWrite):
			return forbidden("the token lacks the messages:write scope")
		case info.Archived:
			return conflict("the channel is archived")
		}
	} else if err := Can(actor, ActDeleteAny, info); err != nil {
		return err
	}
	if ref.Deleted {
		return nil
	}
	now := tx.Now()
	_, err = tx.Exec(`UPDATE messages SET body = '', body_plain = '', mention_channel = 0, mention_here = 0, deleted_at = ? WHERE id = ?`, now, ref.ID)
	if err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	for _, table := range []string{"reactions", "mentions", "attachments"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE message_id = ?`, ref.ID); err != nil {
			return fmt.Errorf("delete message %s: %w", table, err)
		}
	}
	res, err := tx.Exec(`DELETE FROM pins WHERE message_id = ?`, ref.ID)
	if err != nil {
		return fmt.Errorf("delete message pins: %w", err)
	}
	tomb, err := loadMessage(ctx, tx, ref.ID)
	if err != nil {
		return err
	}
	tomb.Alert = nil
	if err := scrubEvents(tx, ref.ChannelID, ref.ID, tomb); err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		err = emit(tx, "pin.removed", ref.ChannelID, "", protocol.PinEvent{ChannelID: ref.ChannelID, MessageID: ref.ID, UserID: actor.ID})
		if err != nil {
			return err
		}
	}
	err = emit(tx, "message.deleted", ref.ChannelID, "", protocol.MessageDeleted{
		ID: ref.ID, ChannelID: ref.ChannelID, ThreadRootID: ref.RootID, DeletedAt: protocol.FormatTime(now),
	})
	if err != nil {
		return err
	}
	if ref.RootID == nil {
		return nil
	}
	return s.refreshThread(tx, ref.ChannelID, *ref.RootID)
}

// scrubEvents rewrites the payloads already stored for a deleted message, so
// its body, files and alert text do not survive in the event log. Message
// events take the tombstone of the message. Reaction and pin events keep only
// their ids and the deleted flag: their reducers key on message_id, and they
// carry no body text of their own.
func scrubEvents(tx *store.Tx, channelID, messageID string, tomb protocol.Message) error {
	msgJSON, err := json.Marshal(tomb)
	if err != nil {
		return fmt.Errorf("encode tombstone: %w", err)
	}
	if _, err := tx.Exec(`UPDATE events SET payload = ?
		WHERE type IN ('message.created', 'message.updated') AND json_extract(payload, '$.id') = ?`,
		string(msgJSON), messageID); err != nil {
		return fmt.Errorf("scrub message events: %w", err)
	}
	stub, err := json.Marshal(struct {
		ChannelID string `json:"channel_id"`
		MessageID string `json:"message_id"`
		Deleted   bool   `json:"deleted"`
	}{channelID, messageID, true})
	if err != nil {
		return fmt.Errorf("encode tombstone: %w", err)
	}
	if _, err := tx.Exec(`UPDATE events SET payload = ?
		WHERE type IN ('reaction.added', 'reaction.removed', 'pin.added', 'pin.removed') AND json_extract(payload, '$.message_id') = ?`,
		string(stub), messageID); err != nil {
		return fmt.Errorf("scrub reaction and pin events: %w", err)
	}
	return nil
}

// GetMessage returns a message from a channel the actor may view. An app needs
// messages:read; a channel it can see but not read is ErrForbidden.
func (s *Service) GetMessage(ctx context.Context, actor Actor, messageID string) (protocol.Message, error) {
	db := s.db.Read()
	ref, err := contentRefForActor(ctx, db, actor, messageID)
	if err != nil {
		return protocol.Message{}, err
	}
	return loadMessage(ctx, db, ref.ID)
}

// ListMessages returns a page of the channel's root messages, oldest first.
// Replies are fetched per thread with ListThread. An app needs messages:read
// (ActReadContent); channels:read alone shows the channel, not its messages.
func (s *Service) ListMessages(ctx context.Context, actor Actor, channelID string, o PageOptions) (protocol.MessagePage, error) {
	db := s.db.Read()
	if _, _, err := authorize(ctx, db, actor, ActReadContent, channelID); err != nil {
		return protocol.MessagePage{}, err
	}
	return pageMessages(ctx, db, `channel_id = ? AND thread_root_id IS NULL`, []any{channelID}, o)
}

// pageMessages returns the page of messages matching base (a WHERE fragment
// over the messages table), anchored by id.
func pageMessages(ctx context.Context, q queryer, base string, baseArgs []any, o PageOptions) (protocol.MessagePage, error) {
	anchors := 0
	for _, a := range []string{o.Before, o.After, o.Around} {
		if a != "" {
			anchors++
		}
	}
	if anchors > 1 {
		return protocol.MessagePage{}, invalid("before", "use only one of before, after and around")
	}
	limit := clampLimit(o.Limit)
	fetch := func(cond string, desc bool, n int, extra ...any) ([]string, error) {
		order := "ASC"
		if desc {
			order = "DESC"
		}
		rows, err := q.QueryContext(ctx, `SELECT id FROM messages WHERE `+base+cond+` ORDER BY id `+order+` LIMIT ?`,
			append(append(append([]any{}, baseArgs...), extra...), n)...)
		if err != nil {
			return nil, fmt.Errorf("list messages: %w", err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("list messages: %w", err)
			}
			ids = append(ids, id)
		}
		return ids, rows.Err()
	}
	var ids []string
	var before, after bool
	var err error
	switch {
	case o.Around != "":
		var ok int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE `+base+` AND id = ?`,
			append(append([]any{}, baseArgs...), o.Around)...).Scan(&ok); err != nil {
			return protocol.MessagePage{}, fmt.Errorf("list messages: %w", err)
		}
		if ok == 0 {
			return protocol.MessagePage{}, notFound()
		}
		half := limit / 2
		older, err := fetch(` AND id <= ?`, true, half+2, o.Around)
		if err != nil {
			return protocol.MessagePage{}, err
		}
		if before = len(older) > half+1; before {
			older = older[:half+1]
		}
		newerCap := limit - (half + 1)
		newer, err := fetch(` AND id > ?`, false, newerCap+1, o.Around)
		if err != nil {
			return protocol.MessagePage{}, err
		}
		if after = len(newer) > newerCap; after {
			newer = newer[:newerCap]
		}
		reverse(older)
		ids = append(older, newer...)
	case o.Before != "":
		if ids, err = fetch(` AND id < ?`, true, limit+1, o.Before); err != nil {
			return protocol.MessagePage{}, err
		}
		if before = len(ids) > limit; before {
			ids = ids[:limit]
		}
		after = true
		reverse(ids)
	case o.After != "":
		if ids, err = fetch(` AND id > ?`, false, limit+1, o.After); err != nil {
			return protocol.MessagePage{}, err
		}
		if after = len(ids) > limit; after {
			ids = ids[:limit]
		}
		before = true
	default:
		if ids, err = fetch(``, true, limit+1); err != nil {
			return protocol.MessagePage{}, err
		}
		if before = len(ids) > limit; before {
			ids = ids[:limit]
		}
		reverse(ids)
	}
	items, err := loadMessageList(ctx, q, ids)
	if err != nil {
		return protocol.MessagePage{}, err
	}
	return protocol.MessagePage{Items: items, HasMoreBefore: before, HasMoreAfter: after}, nil
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
