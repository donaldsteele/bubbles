package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/donaldsteele/bubbles/internal/store"
)

// Kinds of user accounts.
const (
	KindHuman = "human"
	KindApp   = "app"
)

// User is an account row without its password hash.
type User struct {
	ID            string
	Username      string
	DisplayName   string
	Kind          string
	Role          Role
	AvatarFileID  *string
	StatusText    string
	Prefs         json.RawMessage
	MustChangePW  bool
	CreatedAt     int64 // unix ms
	DeactivatedAt *int64
}

// Active reports whether the account may sign in.
func (u *User) Active() bool { return u.DeactivatedAt == nil }

// userColumns lists the users columns scanUser reads; prefix is "u." or "".
func userColumns(prefix string) string {
	cols := []string{"id", "username", "display_name", "kind", "role", "avatar_file_id", "status_text", "prefs", "must_change_pw", "created_at", "deactivated_at"}
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += prefix + c
	}
	return out
}

type scanner interface{ Scan(dest ...any) error }

func scanUser(sc scanner, extra ...any) (*User, error) {
	var (
		u      User
		role   string
		avatar sql.NullString
		prefs  string
		mustCh int
		deact  sql.NullInt64
	)
	dest := append([]any{&u.ID, &u.Username, &u.DisplayName, &u.Kind, &role, &avatar, &u.StatusText, &prefs, &mustCh, &u.CreatedAt, &deact}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	u.Role = Role(role)
	if avatar.Valid {
		u.AvatarFileID = &avatar.String
	}
	u.Prefs = json.RawMessage(prefs)
	u.MustChangePW = mustCh == 1
	if deact.Valid {
		u.DeactivatedAt = &deact.Int64
	}
	return &u, nil
}

// UserByID returns the user, or ErrNotFound.
func (s *Service) UserByID(ctx context.Context, id string) (*User, error) {
	u, err := scanUser(s.db.Read().QueryRowContext(ctx, `SELECT `+userColumns("")+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}
	return u, nil
}

// UserByUsername returns the user (case-insensitive), or ErrNotFound.
func (s *Service) UserByUsername(ctx context.Context, username string) (*User, error) {
	u, err := scanUser(s.db.Read().QueryRowContext(ctx, `SELECT `+userColumns("")+` FROM users WHERE username = ?`, NormalizeUsername(username)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}
	return u, nil
}

// ListUsers returns every account ordered by creation.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT `+userColumns("")+` FROM users ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		out = append(out, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return out, nil
}

// insertUser creates a human account inside tx. The caller has validated the
// fields; a taken username returns ErrUsernameTaken.
func insertUser(tx *store.Tx, f newUserFields, role Role, pwHash string) (*User, error) {
	var exists int
	err := tx.QueryRow(`SELECT 1 FROM users WHERE username = ?`, f.username).Scan(&exists)
	if err == nil {
		return nil, ErrUsernameTaken
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("check username: %w", err)
	}
	u := &User{
		ID: tx.NewID(), Username: f.username, DisplayName: f.displayName, Kind: KindHuman,
		Role: role, Prefs: json.RawMessage("{}"), CreatedAt: tx.Now(),
	}
	if _, err := tx.Exec(`INSERT INTO users (id, username, display_name, kind, role, pw_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, u.ID, u.Username, u.DisplayName, u.Kind, string(u.Role), pwHash, u.CreatedAt); err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

// CountUsers returns the number of accounts.
func (s *Service) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}
