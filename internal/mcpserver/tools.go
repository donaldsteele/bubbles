package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// ToolNames are the tools the server registers, in registration order.
var ToolNames = []string{
	"list_channels", "read_messages", "search_messages", "post_message",
	"list_alerts", "post_alert", "ack_alert", "resolve_alert",
}

const (
	defaultReadLimit = 50
	maxReadLimit     = 100
)

type tools struct{ b Backend }

func ptr[T any](v T) *T { return &v }

func registerTools(s *mcp.Server, t *tools) {
	ro := func(title string) *mcp.ToolAnnotations {
		return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: ptr(false)}
	}
	wr := func(title string, idempotent bool) *mcp.ToolAnnotations {
		return &mcp.ToolAnnotations{Title: title, DestructiveHint: ptr(false), IdempotentHint: idempotent, OpenWorldHint: ptr(false)}
	}
	mcp.AddTool(s, &mcp.Tool{Name: "list_channels", Title: "List channels",
		Description: "List the channels the bot belongs to. Needs scopes mcp and messages:read.",
		Annotations: ro("List channels")}, t.listChannels)
	mcp.AddTool(s, &mcp.Tool{Name: "read_messages", Title: "Read messages",
		Description: "Read the latest messages of a channel, oldest first. Page back with next_before. Needs scopes mcp and messages:read.",
		Annotations: ro("Read messages")}, t.readMessages)
	mcp.AddTool(s, &mcp.Tool{Name: "search_messages", Title: "Search messages",
		Description: "Full-text search over the bot's channels; optionally within one channel. Needs scopes mcp and messages:read.",
		Annotations: ro("Search messages")}, t.searchMessages)
	mcp.AddTool(s, &mcp.Tool{Name: "post_message", Title: "Post a message",
		Description: "Post a message as the bot, optionally as a thread reply. Needs scopes mcp and messages:write.",
		Annotations: wr("Post a message", false)}, t.postMessage)
	mcp.AddTool(s, &mcp.Tool{Name: "list_alerts", Title: "List alerts",
		Description: "List alerts, newest first, optionally by state or channel. Needs scopes mcp, alerts:write and messages:read.",
		Annotations: ro("List alerts")}, t.listAlerts)
	mcp.AddTool(s, &mcp.Tool{Name: "post_alert", Title: "Post an alert",
		Description: "Create an alert, or update the open alert with the same dedupe_key. Needs scopes mcp and alerts:write.",
		Annotations: wr("Post an alert", true)}, t.postAlert)
	mcp.AddTool(s, &mcp.Tool{Name: "ack_alert", Title: "Acknowledge an alert",
		Description: "Acknowledge an alert by message id. App tokens are refused by the server: acknowledging is for people. Needs scopes mcp and alerts:write.",
		Annotations: wr("Acknowledge an alert", true)}, t.ackAlert)
	mcp.AddTool(s, &mcp.Tool{Name: "resolve_alert", Title: "Resolve an alert",
		Description: "Resolve the bot's own open alert by dedupe_key (resolving by id is refused for app tokens). Needs scopes mcp and alerts:write.",
		Annotations: wr("Resolve an alert", true)}, t.resolveAlert)
}

// need checks the scopes before any backend call; "mcp" is always required.
func (t *tools) need(ctx context.Context, scopes ...string) error {
	have, err := t.b.Scopes(ctx)
	if err != nil {
		return err
	}
	for _, s := range append([]string{protocol.ScopeMCP}, scopes...) {
		if !slices.Contains(have, s) {
			return fmt.Errorf("the token lacks the %s scope", s)
		}
	}
	return nil
}

var ulidRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// channel resolves a name ("general", "#general") or id to a channel from the
// bot's own channel list. An id is passed through unchanged when the listing
// is not available to the token (the services then decide whether the bot may
// use it); its name is empty then.
func (t *tools) channel(ctx context.Context, ref string) (id, name string, err error) {
	ref = strings.TrimSpace(ref)
	bare := strings.TrimPrefix(ref, "#")
	if bare == "" {
		return "", "", errors.New("channel is required")
	}
	list, lerr := t.b.ListChannels(ctx)
	if lerr == nil {
		for _, c := range list {
			cn := ""
			if c.Name != nil {
				cn = *c.Name
			}
			if c.ID == ref || (cn != "" && strings.EqualFold(cn, bare)) {
				return c.ID, cn, nil
			}
		}
	}
	if lerr != nil {
		if ulidRe.MatchString(ref) {
			return ref, "", nil
		}
		return "", "", lerr
	}
	return "", "", fmt.Errorf("channel %q not found", ref)
}

// Output types are compact views of the protocol types.

type channelOut struct {
	ID          string `json:"id" jsonschema:"channel id"`
	Name        string `json:"name,omitempty" jsonschema:"channel name without #; empty for direct messages"`
	Kind        string `json:"kind" jsonschema:"public, private, dm or group_dm"`
	Topic       string `json:"topic,omitempty"`
	MemberCount int    `json:"member_count"`
	Archived    bool   `json:"archived,omitempty"`
}

type alertOut struct {
	Severity    string `json:"severity"`
	State       string `json:"state" jsonschema:"open, acked or resolved"`
	Title       string `json:"title"`
	DedupeKey   string `json:"dedupe_key"`
	Occurrences int    `json:"occurrences"`
	LastSeenAt  string `json:"last_seen_at"`
}

type messageOut struct {
	ID           string    `json:"id"`
	ChannelID    string    `json:"channel_id"`
	ThreadRootID string    `json:"thread_root_id,omitempty"`
	Author       string    `json:"author" jsonschema:"display name of the author"`
	AuthorKind   string    `json:"author_kind" jsonschema:"human or app"`
	Kind         string    `json:"kind"`
	Body         string    `json:"body"`
	CreatedAt    string    `json:"created_at"`
	EditedAt     string    `json:"edited_at,omitempty"`
	ReplyCount   int       `json:"reply_count,omitempty"`
	Alert        *alertOut `json:"alert,omitempty"`
}

func toMessage(m protocol.Message) messageOut {
	o := messageOut{
		ID: m.ID, ChannelID: m.ChannelID, Author: m.Author.DisplayName, AuthorKind: m.Author.Kind,
		Kind: m.Kind, Body: m.Body, CreatedAt: m.CreatedAt, ReplyCount: m.ReplyCount,
	}
	if o.Author == "" {
		o.Author = m.Author.Username
	}
	if m.ThreadRootID != nil {
		o.ThreadRootID = *m.ThreadRootID
	}
	if m.EditedAt != nil {
		o.EditedAt = *m.EditedAt
	}
	if a := m.Alert; a != nil {
		o.Alert = &alertOut{Severity: a.Severity, State: a.State, Title: a.Title, DedupeKey: a.DedupeKey, Occurrences: a.Occurrences, LastSeenAt: a.LastSeenAt}
	}
	return o
}

func toMessages(ms []protocol.Message) []messageOut {
	out := make([]messageOut, 0, len(ms))
	for _, m := range ms {
		out = append(out, toMessage(m))
	}
	return out
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func oneLine(m messageOut) string {
	body := strings.ReplaceAll(strings.TrimSpace(m.Body), "\n", " ")
	if r := []rune(body); len(r) > 200 {
		body = string(r[:200]) + "..."
	}
	return fmt.Sprintf("[%s] %s: %s (id %s)", m.CreatedAt, m.Author, body, m.ID)
}

func lines(header string, msgs []messageOut) string {
	var b strings.Builder
	b.WriteString(header)
	for _, m := range msgs {
		b.WriteString("\n" + oneLine(m))
	}
	return b.String()
}

// list_channels

type noInput struct{}

type listChannelsOut struct {
	Channels []channelOut `json:"channels"`
}

func (t *tools) listChannels(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, listChannelsOut, error) {
	out := listChannelsOut{Channels: []channelOut{}}
	if err := t.need(ctx, protocol.ScopeMessagesRead); err != nil {
		return nil, out, err
	}
	list, err := t.b.ListChannels(ctx)
	if err != nil {
		return nil, out, err
	}
	var names []string
	for _, c := range list {
		o := channelOut{ID: c.ID, Kind: c.Kind, Topic: c.Topic, MemberCount: c.MemberCount, Archived: c.ArchivedAt != nil}
		label := c.ID
		if c.Name != nil {
			o.Name, label = *c.Name, "#"+*c.Name
		}
		names = append(names, label)
		out.Channels = append(out.Channels, o)
	}
	return text(fmt.Sprintf("%d channels: %s", len(names), strings.Join(names, ", "))), out, nil
}

// read_messages

type readMessagesIn struct {
	Channel string `json:"channel" jsonschema:"channel name (general or #general) or id"`
	Limit   int    `json:"limit,omitempty" jsonschema:"number of messages, 1 to 100 (default 50)"`
	Before  string `json:"before,omitempty" jsonschema:"message id: return only older messages (use next_before of the previous page)"`
}

type readMessagesOut struct {
	ChannelID  string       `json:"channel_id"`
	Messages   []messageOut `json:"messages" jsonschema:"oldest first"`
	NextBefore string       `json:"next_before,omitempty" jsonschema:"pass as before to read older messages; absent on the last page"`
}

func (t *tools) readMessages(ctx context.Context, _ *mcp.CallToolRequest, in readMessagesIn) (*mcp.CallToolResult, readMessagesOut, error) {
	out := readMessagesOut{Messages: []messageOut{}}
	if err := t.need(ctx, protocol.ScopeMessagesRead); err != nil {
		return nil, out, err
	}
	limit := in.Limit
	switch {
	case limit == 0:
		limit = defaultReadLimit
	case limit < 0 || limit > maxReadLimit:
		return nil, out, fmt.Errorf("limit must be from 1 to %d", maxReadLimit)
	}
	id, _, err := t.channel(ctx, in.Channel)
	if err != nil {
		return nil, out, err
	}
	page, err := t.b.ReadMessages(ctx, id, strings.TrimSpace(in.Before), limit)
	if err != nil {
		return nil, out, err
	}
	out.ChannelID, out.Messages = id, toMessages(page.Items)
	if page.HasMoreBefore && len(out.Messages) > 0 {
		out.NextBefore = out.Messages[0].ID
	}
	return text(lines(fmt.Sprintf("%d messages, oldest first", len(out.Messages)), out.Messages)), out, nil
}

// search_messages

type searchIn struct {
	Query   string `json:"query" jsonschema:"search words; supports \"exact phrase\", from:@user, has:file|image|link"`
	Channel string `json:"channel,omitempty" jsonschema:"limit to one channel: name (general or #general) or id"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"next_cursor of the previous page, to get the next results"`
}

type searchHitOut struct {
	Message messageOut `json:"message"`
	Snippet string     `json:"snippet"`
}

type searchOut struct {
	Results    []searchHitOut `json:"results" jsonschema:"best match first"`
	NextCursor string         `json:"next_cursor,omitempty" jsonschema:"pass as cursor to get the next page; absent on the last page"`
}

func (t *tools) searchMessages(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
	out := searchOut{Results: []searchHitOut{}}
	if err := t.need(ctx, protocol.ScopeMessagesRead); err != nil {
		return nil, out, err
	}
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return nil, out, errors.New("query is required")
	}
	if strings.TrimSpace(in.Channel) != "" {
		_, name, err := t.channel(ctx, in.Channel)
		if err != nil {
			return nil, out, err
		}
		if name == "" {
			return nil, out, errors.New("search can only be limited to a named channel")
		}
		q += " in:#" + name
	}
	page, err := t.b.Search(ctx, q, strings.TrimSpace(in.Cursor))
	if err != nil {
		return nil, out, err
	}
	var msgs []messageOut
	for _, r := range page.Items {
		m := toMessage(r.Message)
		msgs = append(msgs, m)
		out.Results = append(out.Results, searchHitOut{Message: m, Snippet: r.Snippet})
	}
	if page.NextCursor != nil {
		out.NextCursor = *page.NextCursor
	}
	return text(lines(fmt.Sprintf("%d results", len(msgs)), msgs)), out, nil
}

// post_message

type postMessageIn struct {
	Channel      string `json:"channel" jsonschema:"channel name (general or #general) or id"`
	Text         string `json:"text" jsonschema:"message text (Markdown)"`
	ThreadRootID string `json:"thread_root_id,omitempty" jsonschema:"id of the thread's first message to reply in the thread"`
}

type messageResult struct {
	Message messageOut `json:"message"`
}

func (t *tools) postMessage(ctx context.Context, _ *mcp.CallToolRequest, in postMessageIn) (*mcp.CallToolResult, messageResult, error) {
	var out messageResult
	if err := t.need(ctx, protocol.ScopeMessagesWrite); err != nil {
		return nil, out, err
	}
	if strings.TrimSpace(in.Text) == "" {
		return nil, out, errors.New("text is required")
	}
	id, _, err := t.channel(ctx, in.Channel)
	if err != nil {
		return nil, out, err
	}
	nm := protocol.NewMessage{Body: in.Text}
	if r := strings.TrimSpace(in.ThreadRootID); r != "" {
		nm.ThreadRootID = &r
	}
	m, err := t.b.PostMessage(ctx, id, nm)
	if err != nil {
		return nil, out, err
	}
	out.Message = toMessage(m)
	return text("posted message " + m.ID), out, nil
}

// list_alerts

type listAlertsIn struct {
	State   string `json:"state,omitempty" jsonschema:"open, acked or resolved (default: any)"`
	Channel string `json:"channel,omitempty" jsonschema:"limit to one channel: name (general or #general) or id"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"next_cursor of the previous page, to get the next alerts"`
}

type listAlertsOut struct {
	Alerts     []messageOut `json:"alerts" jsonschema:"newest first"`
	NextCursor string       `json:"next_cursor,omitempty" jsonschema:"pass as cursor to get the next page; absent on the last page"`
}

func (t *tools) listAlerts(ctx context.Context, _ *mcp.CallToolRequest, in listAlertsIn) (*mcp.CallToolResult, listAlertsOut, error) {
	out := listAlertsOut{Alerts: []messageOut{}}
	if err := t.need(ctx, protocol.ScopeAlertsWrite, protocol.ScopeMessagesRead); err != nil {
		return nil, out, err
	}
	var channelID string
	if strings.TrimSpace(in.Channel) != "" {
		id, _, err := t.channel(ctx, in.Channel)
		if err != nil {
			return nil, out, err
		}
		channelID = id
	}
	page, err := t.b.ListAlerts(ctx, strings.TrimSpace(in.State), channelID, strings.TrimSpace(in.Cursor))
	if err != nil {
		return nil, out, err
	}
	out.Alerts = toMessages(page.Items)
	if page.NextCursor != nil {
		out.NextCursor = *page.NextCursor
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d alerts", len(out.Alerts))
	for _, m := range out.Alerts {
		if m.Alert != nil {
			fmt.Fprintf(&b, "\n[%s] %s %q key %s (id %s)", m.Alert.State, m.Alert.Severity, m.Alert.Title, m.Alert.DedupeKey, m.ID)
		}
	}
	return text(b.String()), out, nil
}

// post_alert

type alertFieldIn struct {
	Name  string `json:"name" jsonschema:"field label"`
	Value string `json:"value" jsonschema:"field value"`
	Short bool   `json:"short,omitempty" jsonschema:"render side by side with other short fields"`
}

type postAlertIn struct {
	Channel        string         `json:"channel" jsonschema:"channel name (ops or #ops) or id"`
	Severity       string         `json:"severity" jsonschema:"info, warning or critical"`
	Title          string         `json:"title" jsonschema:"one line summary, at most 200 characters"`
	Text           string         `json:"text,omitempty" jsonschema:"details, at most 4000 characters"`
	Fields         []alertFieldIn `json:"fields,omitempty" jsonschema:"up to 20 name/value pairs"`
	Link           string         `json:"link,omitempty" jsonschema:"http(s) URL with more information"`
	DedupeKey      string         `json:"dedupe_key" jsonschema:"identifies the condition: a repeat updates the open alert instead of creating a new one"`
	RenotifyAfterS *int           `json:"renotify_after_s,omitempty" jsonschema:"seconds before a still-open alert notifies again; 0 turns it off"`
	OccurredAt     string         `json:"occurred_at,omitempty" jsonschema:"RFC 3339 time the condition was seen (default: now)"`
}

type postAlertOut struct {
	Message    messageOut `json:"message"`
	Transition string     `json:"transition" jsonschema:"what the post did: created, or an update of the open alert"`
}

func (t *tools) postAlert(ctx context.Context, _ *mcp.CallToolRequest, in postAlertIn) (*mcp.CallToolResult, postAlertOut, error) {
	var out postAlertOut
	if err := t.need(ctx, protocol.ScopeAlertsWrite); err != nil {
		return nil, out, err
	}
	ai := protocol.AlertInput{
		Channel: strings.TrimSpace(in.Channel), Severity: in.Severity, Title: in.Title, Text: in.Text,
		DedupeKey: in.DedupeKey, RenotifyAfterS: in.RenotifyAfterS,
	}
	for _, f := range in.Fields {
		ai.Fields = append(ai.Fields, protocol.AlertField{Name: f.Name, Value: f.Value, Short: f.Short})
	}
	if in.Link != "" {
		ai.Link = &in.Link
	}
	if in.OccurredAt != "" {
		ai.OccurredAt = &in.OccurredAt
	}
	res, err := t.b.PostAlert(ctx, ai)
	if err != nil {
		return nil, out, err
	}
	out.Message, out.Transition = toMessage(res.Message), res.Transition
	return text(fmt.Sprintf("alert %s (%s), message %s", in.DedupeKey, res.Transition, res.Message.ID)), out, nil
}

// ack_alert

type ackAlertIn struct {
	ID string `json:"id" jsonschema:"message id of the alert"`
}

func (t *tools) ackAlert(ctx context.Context, _ *mcp.CallToolRequest, in ackAlertIn) (*mcp.CallToolResult, messageResult, error) {
	var out messageResult
	if err := t.need(ctx, protocol.ScopeAlertsWrite); err != nil {
		return nil, out, err
	}
	if strings.TrimSpace(in.ID) == "" {
		return nil, out, errors.New("id is required")
	}
	m, err := t.b.AckAlert(ctx, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, out, err
	}
	out.Message = toMessage(m)
	return text("acknowledged alert " + m.ID), out, nil
}

// resolve_alert

type resolveAlertIn struct {
	ID        string `json:"id,omitempty" jsonschema:"message id of the alert (refused for app tokens; prefer dedupe_key)"`
	DedupeKey string `json:"dedupe_key,omitempty" jsonschema:"dedupe_key of the bot's own open alert"`
	Text      string `json:"text,omitempty" jsonschema:"optional resolution note"`
}

func (t *tools) resolveAlert(ctx context.Context, _ *mcp.CallToolRequest, in resolveAlertIn) (*mcp.CallToolResult, messageResult, error) {
	var out messageResult
	if err := t.need(ctx, protocol.ScopeAlertsWrite); err != nil {
		return nil, out, err
	}
	id, key := strings.TrimSpace(in.ID), strings.TrimSpace(in.DedupeKey)
	if (id == "") == (key == "") {
		return nil, out, errors.New("give exactly one of id and dedupe_key")
	}
	m, err := t.b.ResolveAlert(ctx, id, key, in.Text)
	if err != nil {
		return nil, out, err
	}
	out.Message = toMessage(m)
	return text("resolved alert " + m.ID), out, nil
}
