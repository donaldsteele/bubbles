// Package chat is the chat domain service: channels, direct messages,
// messages, threads, reactions, pins, read state and the bootstrap snapshot.
//
// Every mutation runs in exactly one store transaction that writes its rows
// and appends the matching events (internal/events) before it commits, so the
// event log and the tables never disagree. Each mutation exists twice: Foo
// opens its own transaction, FooTx joins the caller's, which is how alerts,
// webhooks and commands reuse the same code path. Callers of a Tx variant own
// the transaction; an error returned from it must be returned from the
// transaction function so nothing partial commits.
package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// FileAttacher links already uploaded files to a new message. It is
// implemented by the files service (T9) and runs inside the message's
// transaction: it must verify the uploader owns the files, insert the
// attachments rows and return the wire view of the files in fileIDs order.
type FileAttacher interface {
	AttachTx(tx *store.Tx, messageID, authorID string, fileIDs []string) ([]protocol.File, error)
}

// Service is the chat domain service.
type Service struct {
	db       *store.DB
	settings *settings.Store

	mu       sync.RWMutex
	attacher FileAttacher
}

// New returns a Service over db. settings supplies the values bootstrap
// reports and the retention ceiling.
func New(db *store.DB, st *settings.Store) *Service {
	return &Service{db: db, settings: st}
}

// SetFileAttacher installs the file attacher. Until one is set, a message
// with file_ids is rejected as a validation error.
func (s *Service) SetFileAttacher(a FileAttacher) {
	s.mu.Lock()
	s.attacher = a
	s.mu.Unlock()
}

func (s *Service) fileAttacher() FileAttacher {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.attacher
}

// queryer is the read side shared by *sql.DB, *sql.Tx and *store.Tx, so the
// loaders serve both read endpoints and in-transaction reads.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type scanner interface{ Scan(dest ...any) error }

// run executes fn in one write transaction and returns its result.
func run[T any](ctx context.Context, s *Service, fn func(tx *store.Tx) (T, error)) (T, error) {
	var out T
	err := s.db.Do(ctx, func(tx *store.Tx) error {
		v, err := fn(tx)
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

// emit appends one channel-scoped (channelID) or user-scoped (userID) event.
func emit(tx *store.Tx, typ, channelID, userID string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", typ, err)
	}
	if _, err := events.Append(tx, events.Event{Type: typ, ChannelID: channelID, UserID: userID, Payload: b}); err != nil {
		return err
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

func timePtr(ms *int64) *string {
	if ms == nil {
		return nil
	}
	return ptr(protocol.FormatTime(*ms))
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// channelRow is a channels row plus its member count.
type channelRow struct {
	ID            string
	Kind          string
	Name          *string
	Topic         string
	Description   string
	DMKey         *string
	IsDefault     bool
	RetentionDays *int
	CreatedBy     *string
	CreatedAt     int64
	ArchivedAt    *int64
	MemberCount   int
}

const channelCols = `c.id, c.kind, c.name, c.topic, c.description, c.dm_key, c.is_default, c.retention_days,
	c.created_by, c.created_at, c.archived_at, (SELECT COUNT(*) FROM memberships mm WHERE mm.channel_id = c.id)`

func scanChannel(sc scanner) (channelRow, error) {
	var (
		r                      channelRow
		name, dmKey, createdBy sql.NullString
		retention, archived    sql.NullInt64
		isDefault              int
	)
	err := sc.Scan(&r.ID, &r.Kind, &name, &r.Topic, &r.Description, &dmKey, &isDefault, &retention,
		&createdBy, &r.CreatedAt, &archived, &r.MemberCount)
	if err != nil {
		return r, err
	}
	if name.Valid {
		r.Name = &name.String
	}
	if dmKey.Valid {
		r.DMKey = &dmKey.String
	}
	if createdBy.Valid {
		r.CreatedBy = &createdBy.String
	}
	if retention.Valid {
		r.RetentionDays = ptr(int(retention.Int64))
	}
	if archived.Valid {
		r.ArchivedAt = &archived.Int64
	}
	r.IsDefault = isDefault == 1
	return r, nil
}

func (r channelRow) wire() protocol.Channel {
	c := protocol.Channel{
		ID: r.ID, Kind: r.Kind, Name: r.Name, Topic: r.Topic, Description: r.Description,
		IsDefault: r.IsDefault, RetentionDays: r.RetentionDays, CreatedBy: r.CreatedBy,
		CreatedAt: protocol.FormatTime(r.CreatedAt), ArchivedAt: timePtr(r.ArchivedAt), MemberCount: r.MemberCount,
	}
	if r.DMKey != nil {
		c.ParticipantIDs = strings.Split(*r.DMKey, ",")
	}
	return c
}

// loadChannel returns the channel or ErrNotFound.
func loadChannel(ctx context.Context, q queryer, id string) (channelRow, error) {
	r, err := scanChannel(q.QueryRowContext(ctx, `SELECT `+channelCols+` FROM channels c WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, notFound()
	}
	if err != nil {
		return r, fmt.Errorf("load channel: %w", err)
	}
	return r, nil
}

// loadChannels runs a channels query that selects channelCols.
func loadChannels(ctx context.Context, q queryer, query string, args ...any) ([]channelRow, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()
	var out []channelRow
	for rows.Next() {
		r, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("list channels: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	return out, nil
}

// memberRole returns the user's role in the channel and whether they are a
// member.
func memberRole(ctx context.Context, q queryer, channelID, userID string) (string, bool, error) {
	var role string
	err := q.QueryRowContext(ctx, `SELECT role FROM memberships WHERE channel_id = ? AND user_id = ?`, channelID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("load membership: %w", err)
	}
	return role, true, nil
}

// channelInfo loads the channel and the actor's relation to it.
func channelInfo(ctx context.Context, q queryer, a Actor, channelID string) (channelRow, ChannelInfo, error) {
	row, err := loadChannel(ctx, q, channelID)
	if err != nil {
		return row, ChannelInfo{}, err
	}
	role, member, err := memberRole(ctx, q, channelID, a.ID)
	if err != nil {
		return row, ChannelInfo{}, err
	}
	return row, ChannelInfo{
		ID: row.ID, Kind: row.Kind, Archived: row.ArchivedAt != nil, Member: member, Owner: member && role == protocol.MemberOwner,
	}, nil
}

// authorize loads the channel and checks the actor may perform act on it.
// A missing channel and an invisible one are indistinguishable (ErrNotFound).
func authorize(ctx context.Context, q queryer, a Actor, act Action, channelID string) (channelRow, ChannelInfo, error) {
	row, info, err := channelInfo(ctx, q, a, channelID)
	if err != nil {
		return row, info, err
	}
	if err := Can(a, act, info); err != nil {
		return row, info, err
	}
	return row, info, nil
}

const membershipCols = `channel_id, user_id, role, joined_at, last_read_id, notify_level, alert_min_severity, muted_until`

func scanMembership(sc scanner) (protocol.Membership, error) {
	var (
		m        protocol.Membership
		joined   int64
		lastRead sql.NullString
		notify   sql.NullString
		muted    sql.NullInt64
	)
	if err := sc.Scan(&m.ChannelID, &m.UserID, &m.Role, &joined, &lastRead, &notify, &m.AlertMinSeverity, &muted); err != nil {
		return m, err
	}
	m.JoinedAt = protocol.FormatTime(joined)
	if lastRead.Valid {
		m.LastReadID = &lastRead.String
	}
	m.NotifyLevel = notify.String
	if muted.Valid {
		m.MutedUntil = ptr(protocol.FormatTime(muted.Int64))
	}
	return m, nil
}

// loadMembership returns the membership; ok is false when there is none.
func loadMembership(ctx context.Context, q queryer, channelID, userID string) (protocol.Membership, bool, error) {
	m, err := scanMembership(q.QueryRowContext(ctx,
		`SELECT `+membershipCols+` FROM memberships WHERE channel_id = ? AND user_id = ?`, channelID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	if err != nil {
		return m, false, fmt.Errorf("load membership: %w", err)
	}
	return m, true, nil
}

// insertMembership adds a member whose read marker starts at the newest
// message, so history from before they joined is not unread.
func insertMembership(tx *store.Tx, channelID, userID, role string) (protocol.Membership, error) {
	_, err := tx.Exec(`INSERT INTO memberships (channel_id, user_id, role, joined_at, last_read_id)
		VALUES (?, ?, ?, ?, (SELECT MAX(id) FROM messages WHERE channel_id = ?))`,
		channelID, userID, role, tx.Now(), channelID)
	if err != nil {
		return protocol.Membership{}, fmt.Errorf("add member: %w", err)
	}
	m, ok, err := loadMembership(tx.Context(), tx, channelID, userID)
	if err != nil {
		return m, err
	}
	if !ok {
		return m, errors.New("add member: membership row missing after insert")
	}
	return m, nil
}

// userState is what the service needs to know about an account.
type userState struct {
	ID          string
	Role        string
	Kind        string
	Deactivated bool
}

// loadUserStates returns the existing accounts among ids, keyed by id.
func loadUserStates(ctx context.Context, q queryer, ids []string) (map[string]userState, error) {
	out := make(map[string]userState, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx,
		`SELECT id, role, kind, deactivated_at IS NOT NULL FROM users WHERE id IN (`+placeholders(len(ids))+`)`, anyArgs(ids)...)
	if err != nil {
		return nil, fmt.Errorf("load users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var u userState
		if err := rows.Scan(&u.ID, &u.Role, &u.Kind, &u.Deactivated); err != nil {
			return nil, fmt.Errorf("load users: %w", err)
		}
		out[u.ID] = u
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func anyArgs(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// dedupe returns ids without duplicates or blanks, in first-seen order.
func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
