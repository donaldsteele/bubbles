package search

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Page sizes: a page holds at most MaxLimit results.
const (
	DefaultLimit = 50
	MaxLimit     = 50
)

// MaxCursorChars bounds the length of a cursor accepted from a client.
const MaxCursorChars = 200

// Service searches the messages of the channels an actor belongs to.
type Service struct {
	db   *store.DB
	chat *chat.Service
	loc  *time.Location
}

// New returns a search Service over db. chat hydrates the hits into wire
// messages. loc is the time zone of before:/after: dates; nil means
// time.Local, the server's zone.
func New(db *store.DB, c *chat.Service, loc *time.Location) *Service {
	if loc == nil {
		loc = time.Local
	}
	return &Service{db: db, chat: c, loc: loc}
}

// Params are the inputs of one search request.
type Params struct {
	// Q is the raw query text (see the package comment for the grammar).
	Q string
	// Cursor is the NextCursor of the previous page; empty for the first page.
	Cursor string
	// Limit is the page size, 1..MaxLimit; 0 means DefaultLimit.
	Limit int
}

// Search returns one page of hits, best match first (newest first for a
// query without text terms). Only messages in channels the actor is a member
// of and may view are searched; deleted and system messages never match.
func (s *Service) Search(ctx context.Context, actor chat.Actor, p Params) (protocol.SearchPage, error) {
	if actor.Class() == "" {
		return protocol.SearchPage{}, &chat.Error{Kind: chat.ErrForbidden, Msg: "unknown account role"}
	}
	if actor.IsApp() && !actor.HasScope(chat.ScopeMessagesRead) {
		return protocol.SearchPage{}, &chat.Error{Kind: chat.ErrForbidden, Msg: "the token lacks the messages:read scope"}
	}
	limit := p.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)

	q, err := ParseQuery(p.Q, s.loc)
	if err != nil {
		return protocol.SearchPage{}, err
	}
	match := q.Match()
	cur, err := decodeCursor(p.Cursor, match != "")
	if err != nil {
		return protocol.SearchPage{}, err
	}

	channels, err := s.channelIDs(ctx, actor, q.Channels)
	if err != nil {
		return protocol.SearchPage{}, err
	}
	empty := protocol.SearchPage{Items: []protocol.SearchResult{}}
	if len(channels) == 0 {
		return empty, nil
	}

	hits, err := s.find(ctx, q, match, channels, cur, limit+1)
	if err != nil {
		return protocol.SearchPage{}, err
	}
	more := len(hits) > limit
	if more {
		hits = hits[:limit]
	}
	if len(hits) == 0 {
		return empty, nil
	}
	snippets, err := s.snippets(ctx, match, hits)
	if err != nil {
		return protocol.SearchPage{}, err
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.id
	}
	msgs, err := s.chat.HydrateMessagesUnchecked(ctx, ids)
	if err != nil {
		return protocol.SearchPage{}, err
	}
	byID := make(map[string]protocol.Message, len(msgs))
	for _, m := range msgs {
		byID[m.ID] = m
	}
	page := protocol.SearchPage{Items: make([]protocol.SearchResult, 0, len(hits))}
	for _, h := range hits {
		m, ok := byID[h.id]
		if !ok || m.Deleted {
			continue // deleted between the two reads
		}
		page.Items = append(page.Items, protocol.SearchResult{Message: m, Snippet: snippets[h.rowid]})
	}
	if more {
		last := hits[len(hits)-1]
		c := encodeCursor(match != "", last.rank, last.id)
		page.NextCursor = &c
	}
	return page, nil
}

// channelIDs returns the ids of the channels to search: those the actor
// belongs to and may view, narrowed to the named ones when in: was given.
// Unknown names match nothing.
func (s *Service) channelIDs(ctx context.Context, actor chat.Actor, names []string) ([]string, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT c.id, c.kind, c.name, c.archived_at IS NOT NULL
		FROM memberships mm JOIN channels c ON c.id = mm.channel_id WHERE mm.user_id = ?`, actor.ID)
	if err != nil {
		return nil, fmt.Errorf("search: list channels: %w", err)
	}
	defer rows.Close()
	wanted := make(map[string]bool, len(names))
	for _, n := range names {
		wanted[n] = true
	}
	var ids []string
	for rows.Next() {
		var (
			info     = chat.ChannelInfo{Member: true}
			name     sql.NullString
			archived bool
		)
		if err := rows.Scan(&info.ID, &info.Kind, &name, &archived); err != nil {
			return nil, fmt.Errorf("search: list channels: %w", err)
		}
		info.Archived = archived
		if chat.Can(actor, chat.ActView, info) != nil {
			continue
		}
		if len(wanted) > 0 && !(name.Valid && wanted[strings.ToLower(name.String)]) {
			continue
		}
		ids = append(ids, info.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: list channels: %w", err)
	}
	return ids, nil
}

// hit is one matching message before hydration.
type hit struct {
	rowid int64
	id    string
	rank  float64
}

// find runs the search and returns up to n hits after cur. All values reach
// the SQL as parameters; only constant fragments are concatenated.
func (s *Service) find(ctx context.Context, q Query, match string, channels []string, cur cursor, n int) ([]hit, error) {
	list, err := json.Marshal(channels)
	if err != nil {
		return nil, fmt.Errorf("search: encode channels: %w", err)
	}
	var (
		args  []any
		where = []string{`m.deleted_at IS NULL`, `m.kind <> 'system'`, `m.channel_id IN (SELECT value FROM json_each(?))`}
		sel   = `SELECT m.rowid, m.id, 0.0 FROM messages m WHERE `
		order = ` ORDER BY m.id DESC LIMIT ?`
	)
	if match != "" {
		// CROSS JOIN pins the full-text table as the outer loop: the MATCH
		// narrows the candidates before any messages row is read.
		sel = `SELECT m.rowid, m.id, f.rank FROM messages_fts f CROSS JOIN messages m ON m.rowid = f.rowid WHERE messages_fts MATCH ? AND `
		order = ` ORDER BY f.rank, m.id DESC LIMIT ?`
		args = append(args, match)
	}
	args = append(args, string(list))
	if len(q.Users) > 0 {
		where = append(where, `m.author_id IN (SELECT id FROM users WHERE username IN (`+placeholders(len(q.Users))+`))`)
		for _, u := range q.Users {
			args = append(args, u)
		}
	}
	if q.HasFile {
		where = append(where, `EXISTS (SELECT 1 FROM attachments a WHERE a.message_id = m.id)`)
	}
	if q.HasImage {
		where = append(where, `EXISTS (SELECT 1 FROM attachments a JOIN files fl ON fl.id = a.file_id
			WHERE a.message_id = m.id AND fl.category = 'image')`)
	}
	if q.HasLink {
		where = append(where, `(m.body LIKE '%http://%' OR m.body LIKE '%https://%')`)
	}
	if q.Before != nil {
		where = append(where, `m.created_at < ?`)
		args = append(args, *q.Before)
	}
	if q.After != nil {
		where = append(where, `m.created_at >= ?`)
		args = append(args, *q.After)
	}
	if cur.set {
		if match != "" {
			where = append(where, `(f.rank > ? OR (f.rank = ? AND m.id < ?))`)
			args = append(args, cur.rank, cur.rank, cur.id)
		} else {
			where = append(where, `m.id < ?`)
			args = append(args, cur.id)
		}
	}
	args = append(args, n)

	rows, err := s.db.Read().QueryContext(ctx, sel+strings.Join(where, ` AND `)+order, args...)
	if err != nil {
		return nil, fmt.Errorf("search: query: %w", err)
	}
	defer rows.Close()
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.rowid, &h.id, &h.rank); err != nil {
			return nil, fmt.Errorf("search: query: %w", err)
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: query: %w", err)
	}
	return hits, nil
}

// snippets returns the snippet HTML of each hit by rowid. snippet() is only
// evaluated for the page's rows (the rowid IN filter), so the highlighting
// cost is bounded by the page size; the MATCH itself is still resolved
// against the full-text index, whose cost grows with the number of matches.
func (s *Service) snippets(ctx context.Context, match string, hits []hit) (map[int64]string, error) {
	in := placeholders(len(hits))
	args := make([]any, 0, len(hits)+4)
	query := `SELECT m.rowid, m.body_plain FROM messages m WHERE m.rowid IN (` + in + `)`
	if match != "" {
		query = `SELECT m.rowid, snippet(messages_fts, 0, ?, ?, '…', ` + strconv.Itoa(snippetTokens) + `)
			FROM messages_fts f CROSS JOIN messages m ON m.rowid = f.rowid
			WHERE messages_fts MATCH ? AND m.rowid IN (` + in + `)`
		args = append(args, markOpen, markClose, match)
	}
	for _, h := range hits {
		args = append(args, h.rowid)
	}
	rows, err := s.db.Read().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search: snippets: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]string, len(hits))
	for rows.Next() {
		var (
			rowid int64
			text  string
		)
		if err := rows.Scan(&rowid, &text); err != nil {
			return nil, fmt.Errorf("search: snippets: %w", err)
		}
		if match != "" {
			out[rowid] = renderSnippet(text)
		} else {
			out[rowid] = excerpt(text)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: snippets: %w", err)
	}
	return out, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// cursor is the position after the last hit of a page.
type cursor struct {
	set  bool
	rank float64
	id   string
}

// encodeCursor makes the opaque cursor "r:<rank bits>:<id>" (ranked, text
// search) or "i::<id>" (id order, filter-only search), base64url encoded.
// The rank travels as its exact IEEE 754 bits so equal ranks stay equal.
func encodeCursor(ranked bool, rank float64, id string) string {
	raw := "i::" + id
	if ranked {
		raw = "r:" + strconv.FormatUint(math.Float64bits(rank), 16) + ":" + id
	}
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(c string, ranked bool) (cursor, error) {
	if c == "" {
		return cursor{}, nil
	}
	bad := &chat.ValidationError{Fields: map[string]string{"cursor": "is not a valid cursor for this query"}}
	if len(c) > MaxCursorChars {
		return cursor{}, bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return cursor{}, bad
	}
	kind, rest, ok := strings.Cut(string(raw), ":")
	if !ok || (kind == "r") != ranked || (kind != "r" && kind != "i") {
		return cursor{}, bad
	}
	bits, id, ok := strings.Cut(rest, ":")
	if !ok || !validID(id) {
		return cursor{}, bad
	}
	out := cursor{set: true, id: id}
	if ranked {
		n, err := strconv.ParseUint(bits, 16, 64)
		if err != nil {
			return cursor{}, bad
		}
		out.rank = math.Float64frombits(n)
		if math.IsNaN(out.rank) || math.IsInf(out.rank, 0) {
			return cursor{}, bad
		}
	} else if bits != "" {
		return cursor{}, bad
	}
	return out, nil
}

// validID reports whether id looks like a message id (1-64 ASCII letters or
// digits).
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}
