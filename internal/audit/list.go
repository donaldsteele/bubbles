package audit

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/donaldsteele/bubbles/internal/store"
)

// Further actions recorded by the admin endpoints.
const (
	ActionUserUpdate   = "user.update"
	ActionTLSReissue   = "tls.reissue"
	ActionChannelAdmin = "channel.admin_update"
)

// List limits.
const (
	DefaultListLimit = 50
	MaxListLimit     = 200
)

// ErrBadCursor means a list cursor was not produced by List.
var ErrBadCursor = errors.New("audit: invalid cursor")

// Record is one audit_log row as read back.
type Record struct {
	ID         string
	At         int64 // unix ms
	ActorID    string
	ActorKind  string
	ActorIP    string
	Action     string
	TargetType string
	TargetID   string
	Detail     json.RawMessage
}

// ListParams selects a page of the log, newest first.
type ListParams struct {
	// Cursor is the NextCursor of the previous page; empty starts at the newest row.
	Cursor string
	// Action keeps only rows with exactly this action.
	Action string
	// Limit is the page size; 0 means DefaultListLimit.
	Limit int
}

// List returns one page of audit rows, newest first, and the cursor of the
// next page (empty at the end). A malformed cursor is ErrBadCursor.
func List(ctx context.Context, db *store.DB, p ListParams) ([]Record, string, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	limit = min(limit, MaxListLimit)
	before := ""
	if p.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(p.Cursor)
		if err != nil || len(raw) == 0 || len(raw) > 64 {
			return nil, "", ErrBadCursor
		}
		before = string(raw)
	}
	query := `SELECT id, at, COALESCE(actor_id, ''), actor_kind, actor_ip, action, target_type, target_id, detail
		FROM audit_log WHERE (? = '' OR id < ?) AND (? = '' OR action = ?) ORDER BY id DESC LIMIT ?`
	rows, err := db.Read().QueryContext(ctx, query, before, before, p.Action, p.Action, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("audit: list: %w", err)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var detail string
		if err := rows.Scan(&r.ID, &r.At, &r.ActorID, &r.ActorKind, &r.ActorIP, &r.Action, &r.TargetType, &r.TargetID, &detail); err != nil {
			return nil, "", fmt.Errorf("audit: list: %w", err)
		}
		r.Detail = json.RawMessage(detail)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", fmt.Errorf("audit: list: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte(out[limit-1].ID))
	}
	return out, next, nil
}

// HasRemoteActor reports whether any row of the given actions was performed
// from an address other than loopback (the client-isolation hint of the
// admin overview).
func HasRemoteActor(ctx context.Context, db *store.DB, actions ...string) (bool, error) {
	for _, a := range actions {
		var one int
		err := db.Read().QueryRowContext(ctx, `SELECT 1 FROM audit_log
			WHERE action = ? AND actor_ip NOT IN ('', '127.0.0.1', '::1') LIMIT 1`, a).Scan(&one)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("audit: remote actor: %w", err)
		}
	}
	return false, nil
}

// SetupTime returns when first-run setup completed (unix milliseconds); ok is
// false while no setup row exists.
func SetupTime(ctx context.Context, db *store.DB) (at int64, ok bool, err error) {
	var n sql.NullInt64
	if err = db.Read().QueryRowContext(ctx, `SELECT MIN(at) FROM audit_log WHERE action = ?`, ActionSetup).Scan(&n); err != nil {
		return 0, false, fmt.Errorf("audit: setup time: %w", err)
	}
	return n.Int64, n.Valid, nil
}
