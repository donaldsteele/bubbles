// Package alerts is the alert domain (plan pipeline step 11): an app posts an
// alert under a dedupe key; repeats update one message instead of flooding the
// channel, escalations and overdue renotifications are marked as such for the
// notification dispatcher, members acknowledge and resolve, and a key that
// was resolved opens a new alert the next time.
//
// Alerts are chat messages (kind alert) plus an alerts row, written through
// internal/chat in one transaction with their events: message.created for a
// new alert, message.updated and alert.updated for a repeat, alert.updated
// for an acknowledgement or resolution.
package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Audit actions written by this package.
const (
	ActionAck     = "alert.ack"
	ActionResolve = "alert.resolve"
)

// Renotify bounds (seconds) for a per-alert override; 0 turns it off.
const (
	MinRenotifyS = 10
	MaxRenotifyS = 86400
)

// Options configure a Service.
type Options struct {
	DB       *store.DB
	Chat     *chat.Service
	Settings *settings.Store
}

// Service is the alerts domain service.
type Service struct {
	db       *store.DB
	chat     *chat.Service
	settings *settings.Store
}

// New returns a Service. All options are required.
func New(o Options) *Service {
	return &Service{db: o.DB, chat: o.Chat, settings: o.Settings}
}

func notFound() error { return &chat.Error{Kind: chat.ErrNotFound, Msg: "not found"} }

func forbidden(msg string) error { return &chat.Error{Kind: chat.ErrForbidden, Msg: msg} }

func conflict(msg string) error { return &chat.Error{Kind: chat.ErrConflict, Msg: msg} }

func invalid(field, reason string) error {
	return &chat.ValidationError{Fields: map[string]string{field: reason}}
}

// PostResult is what a post did.
type PostResult struct {
	Message protocol.Message
	// Transition is created for a new alert, else escalated, renotify_due or
	// updated_other.
	Transition string
}

// Created reports whether the post opened a new alert.
func (r PostResult) Created() bool { return r.Transition == protocol.AlertTransitionCreated }

// input is a validated AlertInput.
type input struct {
	channel   string
	severity  string
	title     string
	text      string
	fields    []protocol.AlertField
	link      string
	dedupeKey string
	renotifyS *int
	occurred  *int64
}

func validate(in protocol.AlertInput) (input, error) {
	fields := map[string]string{}
	out := input{channel: strings.TrimSpace(in.Channel), severity: in.Severity, dedupeKey: in.DedupeKey, renotifyS: in.RenotifyAfterS}
	if out.channel == "" || len(out.channel) > 80 || !validText(out.channel) {
		fields["channel"] = "channel is required (a channel name or id)"
	}
	if rank(in.Severity) == 0 {
		fields["severity"] = "severity must be info, warning or critical"
	}
	out.title = strings.TrimSpace(in.Title)
	switch {
	case collapseBlank(out.title) == "":
		fields["title"] = "title is required"
	case !validText(out.title):
		fields["title"] = "title must be valid text"
	case utf8.RuneCountInString(out.title) > protocol.MaxAlertTitle:
		fields["title"] = fmt.Sprintf("title must be at most %d characters", protocol.MaxAlertTitle)
	}
	out.text = in.Text
	switch {
	case !validText(out.text):
		fields["text"] = "text must be valid text"
	case utf8.RuneCountInString(out.text) > protocol.MaxAlertText:
		fields["text"] = fmt.Sprintf("text must be at most %d characters", protocol.MaxAlertText)
	}
	switch {
	case in.DedupeKey == "":
		fields["dedupe_key"] = "dedupe_key is required"
	case !validText(in.DedupeKey):
		fields["dedupe_key"] = "dedupe_key must be valid text"
	case utf8.RuneCountInString(in.DedupeKey) > protocol.MaxAlertDedupeKey:
		fields["dedupe_key"] = fmt.Sprintf("dedupe_key must be at most %d characters", protocol.MaxAlertDedupeKey)
	}
	if len(in.Fields) > protocol.MaxAlertFields {
		fields["fields"] = fmt.Sprintf("at most %d fields", protocol.MaxAlertFields)
	} else {
		out.fields = make([]protocol.AlertField, 0, len(in.Fields))
		for i, f := range in.Fields {
			key := fmt.Sprintf("fields[%d]", i)
			switch {
			case strings.TrimSpace(f.Name) == "" || !validText(f.Name) || utf8.RuneCountInString(f.Name) > protocol.MaxAlertFieldName:
				fields[key] = fmt.Sprintf("name is required, at most %d characters", protocol.MaxAlertFieldName)
			case !validText(f.Value) || utf8.RuneCountInString(f.Value) > protocol.MaxAlertFieldValue:
				fields[key] = fmt.Sprintf("value must be at most %d characters", protocol.MaxAlertFieldValue)
			default:
				out.fields = append(out.fields, protocol.AlertField{Name: f.Name, Value: f.Value, Short: f.Short})
			}
		}
	}
	if in.Link != nil {
		if !validLink(*in.Link) {
			fields["link"] = "link must be an http or https URL of at most 2048 characters"
		} else {
			out.link = *in.Link
		}
	}
	if n := in.RenotifyAfterS; n != nil && *n != 0 && (*n < MinRenotifyS || *n > MaxRenotifyS) {
		fields["renotify_after_s"] = fmt.Sprintf("renotify_after_s must be 0 or %d-%d", MinRenotifyS, MaxRenotifyS)
	}
	if in.OccurredAt != nil {
		t, err := time.Parse(time.RFC3339Nano, *in.OccurredAt)
		if err != nil {
			fields["occurred_at"] = "occurred_at must be an RFC 3339 timestamp"
		} else {
			ms := t.UnixMilli()
			out.occurred = &ms
		}
	}
	if len(fields) > 0 {
		return out, &chat.ValidationError{Fields: fields}
	}
	return out, nil
}

func validText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

func collapseBlank(s string) string {
	return strings.TrimFunc(s, unicode.IsSpace)
}

// Post ingests an alert from an app. See the package comment for the rules.
// The caller must be an app whose token carries alerts:write; the app must be
// a member of the target channel (and the token's channel allowlist must
// admit it), else the channel is ErrNotFound.
func (s *Service) Post(ctx context.Context, actor chat.Actor, in protocol.AlertInput) (PostResult, error) {
	return s.PostWith(ctx, actor, in, PostOptions{})
}

// PostOptions tune PostWith.
type PostOptions struct {
	// DisableMentions keeps the alert message free of mentions, whatever its
	// text says. Webhooks set it: their text comes from outside.
	DisableMentions bool
}

// PostWith is Post with options.
func (s *Service) PostWith(ctx context.Context, actor chat.Actor, in protocol.AlertInput, o PostOptions) (PostResult, error) {
	if err := requireApp(actor); err != nil {
		return PostResult{}, err
	}
	norm, err := validate(in)
	if err != nil {
		return PostResult{}, err
	}
	var res PostResult
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		var err error
		res, err = s.postTx(tx, actor, norm, o)
		return err
	})
	return res, err
}

// requireApp is the gate of every app-only alert operation.
func requireApp(actor chat.Actor) error {
	if !actor.IsApp() {
		return forbidden("only apps can do this")
	}
	if !actor.HasScope(protocol.ScopeAlertsWrite) {
		return forbidden("the token lacks the alerts:write scope")
	}
	return nil
}

func (s *Service) postTx(tx *store.Tx, actor chat.Actor, in input, o PostOptions) (PostResult, error) {
	appID, err := appOf(tx, actor.ID)
	if err != nil {
		return PostResult{}, err
	}
	channelID, archived, err := resolveChannel(tx, in.channel)
	if err != nil {
		return PostResult{}, err
	}
	if err := checkAccess(tx, actor, channelID); err != nil {
		return PostResult{}, err
	}
	if archived {
		return PostResult{}, conflict("the channel is archived")
	}
	now := tx.Now()
	seen := now
	if in.occurred != nil {
		seen = min(*in.occurred, now)
	}
	body := Render(in.title, in.text, in.fields, in.link)
	fieldsJSON, err := json.Marshal(in.fields)
	if err != nil {
		return PostResult{}, fmt.Errorf("encode alert fields: %w", err)
	}
	var link any
	if in.link != "" {
		link = in.link
	}
	var renotify any
	if in.renotifyS != nil {
		renotify = *in.renotifyS
	}

	old, found, err := open(tx, appID, in.dedupeKey)
	if err != nil {
		return PostResult{}, err
	}
	if !found {
		msg, err := s.chat.CreateMessageTx(tx, chat.CreateMessageParams{
			ChannelID: channelID, AuthorID: actor.ID, Kind: protocol.MessageAlert, Body: body, DisableMentions: o.DisableMentions,
			AfterInsert: func(tx *store.Tx, messageID string) error {
				_, err := tx.Exec(`INSERT INTO alerts (message_id, app_id, dedupe_key, severity, title, fields, link, first_seen_at,
						last_seen_at, last_notified_at, renotify_after_s)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					messageID, appID, in.dedupeKey, in.severity, in.title, string(fieldsJSON), link, seen, seen, now, renotify)
				if err != nil {
					return fmt.Errorf("insert alert: %w", err)
				}
				return nil
			},
		})
		if err != nil {
			return PostResult{}, err
		}
		return PostResult{Message: msg, Transition: protocol.AlertTransitionCreated}, nil
	}

	if old.ChannelID != channelID {
		return PostResult{}, conflict("an alert with this dedupe_key is already open in another channel")
	}
	trans := transition(old, in.severity, now, s.settings.AlertRenotifyS())
	severity := old.Severity
	if rank(in.severity) > rank(severity) {
		severity = in.severity
	}
	notified := old.LastNotified
	if trans == protocol.AlertTransitionEscalated || trans == protocol.AlertTransitionRenotifyDue {
		notified = sql.NullInt64{Int64: now, Valid: true}
	}
	renotifyCol := old.RenotifyS
	if in.renotifyS != nil {
		renotifyCol = sql.NullInt64{Int64: int64(*in.renotifyS), Valid: true}
	}
	_, err = tx.Exec(`UPDATE alerts SET severity = ?, title = ?, fields = ?, link = ?, occurrences = occurrences + 1,
			last_seen_at = ?, last_notified_at = ?, renotify_after_s = ? WHERE message_id = ?`,
		severity, in.title, string(fieldsJSON), link, max(old.LastSeenAt, seen), notified, renotifyCol, old.MessageID)
	if err != nil {
		return PostResult{}, fmt.Errorf("update alert: %w", err)
	}
	setBody := s.chat.SetBodyTx
	if o.DisableMentions {
		setBody = s.chat.SetBodyQuietTx
	}
	if err := setBody(tx, old.MessageID, body); err != nil {
		return PostResult{}, err
	}
	msg, err := s.chat.LoadMessageTx(tx, old.MessageID)
	if err != nil {
		return PostResult{}, err
	}
	if err := emit(tx, events.TypeMessageUpdated, channelID, msg); err != nil {
		return PostResult{}, err
	}
	if err := emit(tx, events.TypeAlertUpdated, channelID, protocol.AlertUpdatedEvent{Message: msg, Transition: trans}); err != nil {
		return PostResult{}, err
	}
	return PostResult{Message: msg, Transition: trans}, nil
}

// emit appends a channel-scoped event.
func emit(tx *store.Tx, typ, channelID string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", typ, err)
	}
	_, err = events.Append(tx, events.Event{Type: typ, ChannelID: channelID, Payload: b})
	return err
}

// appOf returns the id of the enabled app behind a bot account.
func appOf(tx *store.Tx, userID string) (string, error) {
	var id string
	var disabled sql.NullInt64
	err := tx.QueryRowContext(tx.Context(), `SELECT id, disabled_at FROM apps WHERE user_id = ?`, userID).Scan(&id, &disabled)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && disabled.Valid) {
		return "", forbidden("this account is not an enabled app")
	}
	if err != nil {
		return "", fmt.Errorf("load app: %w", err)
	}
	return id, nil
}

// resolveChannel finds a public or private channel by id or by name (a
// leading # and letter case are ignored).
func resolveChannel(tx *store.Tx, ref string) (id string, archived bool, err error) {
	var arch sql.NullInt64
	err = tx.QueryRowContext(tx.Context(), `SELECT id, archived_at FROM channels WHERE id = ?`, ref).Scan(&id, &arch)
	if errors.Is(err, sql.ErrNoRows) {
		name := strings.ToLower(strings.TrimPrefix(ref, "#"))
		err = tx.QueryRowContext(tx.Context(), `SELECT id, archived_at FROM channels WHERE name = ? AND kind IN ('public', 'private')`, name).Scan(&id, &arch)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, notFound()
	}
	if err != nil {
		return "", false, fmt.Errorf("find channel: %w", err)
	}
	return id, arch.Valid, nil
}

// checkAccess lets actor act on alerts of channelID: a member of the channel
// and, for an app, within the token's channel allowlist and carrying
// alerts:write. A channel the actor cannot see is ErrNotFound.
func checkAccess(tx *store.Tx, actor chat.Actor, channelID string) error {
	var one int
	err := tx.QueryRowContext(tx.Context(), `SELECT 1 FROM memberships WHERE channel_id = ? AND user_id = ?`, channelID, actor.ID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound()
	}
	if err != nil {
		return fmt.Errorf("check membership: %w", err)
	}
	if actor.IsApp() {
		if actor.ChannelIDs != nil && !slices.Contains(actor.ChannelIDs, channelID) {
			return notFound()
		}
		if !actor.HasScope(protocol.ScopeAlertsWrite) {
			return forbidden("the token lacks the alerts:write scope")
		}
	}
	return nil
}

// rejectAppByID refuses apps on the message-id operations (Ack, Resolve). An
// app acts on its own alerts only through ResolveByKey, whose lookup is
// scoped to the app, so it can never touch another app's alert by id.
func rejectAppByID(actor chat.Actor) error {
	if actor.IsApp() {
		return forbidden("apps act on alerts by dedupe key, not by message id")
	}
	return nil
}

// Ack acknowledges an open alert for the actor, a member of its channel. The
// alert stops renotifying. Acknowledging an acknowledged alert does nothing;
// a resolved one is ErrConflict. Apps are refused (see rejectAppByID).
func (s *Service) Ack(ctx context.Context, actor chat.Actor, aud audit.Actor, messageID string) (protocol.Message, error) {
	if err := rejectAppByID(actor); err != nil {
		return protocol.Message{}, err
	}
	var out protocol.Message
	err := s.db.Do(ctx, func(tx *store.Tx) error {
		r, err := byID(tx.Context(), tx, messageID)
		if err != nil {
			return err
		}
		if r.Deleted {
			return notFound()
		}
		if err := checkAccess(tx, actor, r.ChannelID); err != nil {
			return err
		}
		switch r.State {
		case protocol.AlertResolved:
			return conflict("the alert is resolved")
		case protocol.AlertAcked:
			out, err = s.chat.LoadMessageTx(tx, messageID)
			return err
		}
		_, err = tx.Exec(`UPDATE alerts SET state = 'acked', acked_by = ?, acked_at = ? WHERE message_id = ?`, actor.ID, tx.Now(), messageID)
		if err != nil {
			return fmt.Errorf("ack alert: %w", err)
		}
		detail := map[string]string{"alert_id": messageID, "channel_id": r.ChannelID, "dedupe_key": r.DedupeKey}
		if _, err := audit.Write(tx, aud.Entry(ActionAck, "alert", messageID, detail)); err != nil {
			return err
		}
		if out, err = s.chat.LoadMessageTx(tx, messageID); err != nil {
			return err
		}
		return emit(tx, events.TypeAlertUpdated, r.ChannelID, protocol.AlertUpdatedEvent{Message: out, Transition: protocol.AlertTransitionAcked})
	})
	return out, err
}

// Resolve resolves the alert of a message for the actor, a member of its
// channel, and posts "Resolved by <name>: <text>" in the alert's thread.
// Resolving a resolved alert returns it unchanged.
func (s *Service) Resolve(ctx context.Context, actor chat.Actor, aud audit.Actor, messageID, text string) (protocol.Message, error) {
	if err := rejectAppByID(actor); err != nil {
		return protocol.Message{}, err
	}
	text, err := cleanText(text)
	if err != nil {
		return protocol.Message{}, err
	}
	var out protocol.Message
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		r, err := byID(tx.Context(), tx, messageID)
		if err != nil {
			return err
		}
		if r.Deleted {
			return notFound()
		}
		out, err = s.resolveTx(tx, actor, aud, r, text)
		return err
	})
	return out, err
}

// ResolveByKey resolves the app's alert for dedupe key. It is idempotent: a
// key whose latest alert is already resolved answers that alert; a key the
// app never posted is ErrNotFound.
func (s *Service) ResolveByKey(ctx context.Context, actor chat.Actor, aud audit.Actor, key, text string) (protocol.Message, error) {
	if err := requireApp(actor); err != nil {
		return protocol.Message{}, err
	}
	if key == "" || utf8.RuneCountInString(key) > protocol.MaxAlertDedupeKey || !validText(key) {
		return protocol.Message{}, invalid("dedupe_key", "dedupe_key is required, at most 200 characters")
	}
	text, err := cleanText(text)
	if err != nil {
		return protocol.Message{}, err
	}
	var out protocol.Message
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		appID, err := appOf(tx, actor.ID)
		if err != nil {
			return err
		}
		r, found, err := open(tx, appID, key)
		if err != nil {
			return err
		}
		if !found {
			if r, found, err = latest(tx, appID, key); err != nil {
				return err
			}
			if !found {
				return notFound()
			}
		}
		out, err = s.resolveTx(tx, actor, aud, r, text)
		return err
	})
	return out, err
}

func (s *Service) resolveTx(tx *store.Tx, actor chat.Actor, aud audit.Actor, r row, text string) (protocol.Message, error) {
	if err := checkAccess(tx, actor, r.ChannelID); err != nil {
		return protocol.Message{}, err
	}
	if r.State == protocol.AlertResolved {
		return s.chat.LoadMessageTx(tx, r.MessageID)
	}
	now := tx.Now()
	_, err := tx.Exec(`UPDATE alerts SET state = 'resolved', resolved_by = ?, resolved_at = ? WHERE message_id = ?`, actor.ID, now, r.MessageID)
	if err != nil {
		return protocol.Message{}, fmt.Errorf("resolve alert: %w", err)
	}
	var display, username string
	if err := tx.QueryRowContext(tx.Context(), `SELECT display_name, username FROM users WHERE id = ?`, actor.ID).Scan(&display, &username); err != nil {
		return protocol.Message{}, fmt.Errorf("load resolver: %w", err)
	}
	name := display
	if name == "" {
		name = username
	}
	body := "Resolved by " + name
	if text != "" {
		body += ": " + text
	}
	_, err = s.chat.CreateMessageTx(tx, chat.CreateMessageParams{
		ChannelID: r.ChannelID, AuthorID: actor.ID, Kind: protocol.MessageSystem, Body: body, ThreadRootID: r.MessageID,
	})
	if err != nil {
		return protocol.Message{}, err
	}
	detail := map[string]string{"alert_id": r.MessageID, "channel_id": r.ChannelID, "dedupe_key": r.DedupeKey}
	if _, err := audit.Write(tx, aud.Entry(ActionResolve, "alert", r.MessageID, detail)); err != nil {
		return protocol.Message{}, err
	}
	msg, err := s.chat.LoadMessageTx(tx, r.MessageID)
	if err != nil {
		return protocol.Message{}, err
	}
	err = emit(tx, events.TypeAlertUpdated, r.ChannelID, protocol.AlertUpdatedEvent{Message: msg, Transition: protocol.AlertTransitionResolved})
	return msg, err
}

// cleanText trims the optional resolve text and checks its limit.
func cleanText(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case !validText(s):
		return "", invalid("text", "text must be valid text")
	case utf8.RuneCountInString(s) > protocol.MaxAlertText:
		return "", invalid("text", fmt.Sprintf("text must be at most %d characters", protocol.MaxAlertText))
	}
	return s, nil
}

// ListParams select a page of alerts.
type ListParams struct {
	// State filters by open, acked or resolved; empty means any.
	State string
	// ChannelID restricts the list to one channel.
	ChannelID string
	// Cursor is the next_cursor of the previous page.
	Cursor string
	// Limit is the page size (default 50, at most 100).
	Limit int
}

// List returns alert messages, newest first, from the channels the actor is a
// member of (for an app also inside its token's allowlist). The page carries
// message bodies, so an app needs messages:read, whatever else it holds.
func (s *Service) List(ctx context.Context, actor chat.Actor, p ListParams) (protocol.AlertPage, error) {
	page := protocol.AlertPage{Items: []protocol.Message{}}
	if actor.IsApp() && !actor.HasScope(protocol.ScopeMessagesRead) {
		return page, forbidden("the token lacks the messages:read scope")
	}
	switch p.State {
	case "", protocol.AlertOpen, protocol.AlertAcked, protocol.AlertResolved:
	default:
		return page, invalid("state", "state must be open, acked or resolved")
	}
	if len(p.Cursor) > 64 || !validText(p.Cursor) {
		return page, invalid("cursor", "is not a valid cursor")
	}
	limit := p.Limit
	if limit <= 0 {
		limit = chat.DefaultPageLimit
	}
	limit = min(limit, chat.MaxPageLimit)

	q := `SELECT m.id FROM alerts a JOIN messages m ON m.id = a.message_id
		JOIN memberships mb ON mb.channel_id = m.channel_id AND mb.user_id = ?
		WHERE m.deleted_at IS NULL`
	args := []any{actor.ID}
	if p.State != "" {
		q += ` AND a.state = ?`
		args = append(args, p.State)
	}
	if p.ChannelID != "" {
		q += ` AND m.channel_id = ?`
		args = append(args, p.ChannelID)
	}
	if p.Cursor != "" {
		q += ` AND m.id < ?`
		args = append(args, p.Cursor)
	}
	if actor.IsApp() && actor.ChannelIDs != nil {
		if len(actor.ChannelIDs) == 0 {
			return page, nil
		}
		q += ` AND m.channel_id IN (?` + strings.Repeat(`,?`, len(actor.ChannelIDs)-1) + `)`
		for _, id := range actor.ChannelIDs {
			args = append(args, id)
		}
	}
	q += ` ORDER BY m.id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.Read().QueryContext(ctx, q, args...)
	if err != nil {
		return page, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return page, fmt.Errorf("list alerts: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return page, fmt.Errorf("list alerts: %w", err)
	}
	if len(ids) > limit {
		ids = ids[:limit]
		page.NextCursor = &ids[len(ids)-1]
	}
	if page.Items, err = s.chat.HydrateMessagesUnchecked(ctx, ids); err != nil {
		return page, err
	}
	if page.Items == nil {
		page.Items = []protocol.Message{}
	}
	return page, nil
}
