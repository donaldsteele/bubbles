package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/store"
)

// Invite defaults and bounds.
const (
	DefaultInviteExpiry = 72 * time.Hour
	MaxInviteExpiry     = 90 * 24 * time.Hour
	MaxInviteUses       = 1000
)

// Invite is one row of the invites table; the code itself is never stored.
type Invite struct {
	ID        string
	CreatedBy string
	Role      Role
	Note      string
	ExpiresAt int64 // unix ms
	MaxUses   int
	UseCount  int
	RevokedAt *int64
	CreatedAt int64
}

// CreateInviteInput is the input of CreateInvite.
type CreateInviteInput struct {
	Role Role
	Note string
	// ExpiresIn defaults to DefaultInviteExpiry.
	ExpiresIn time.Duration
	// MaxUses defaults to 1.
	MaxUses int
}

// CreateInvite stores a new invite and returns it with its one-time code,
// which cannot be recovered afterwards.
func (s *Service) CreateInvite(ctx context.Context, actor audit.Actor, in CreateInviteInput) (*Invite, string, error) {
	fields := map[string]string{}
	if in.Role == "" {
		in.Role = RoleMember
	}
	if !in.Role.Valid() {
		fields["role"] = "must be admin, member or guest"
	}
	if in.ExpiresIn == 0 {
		in.ExpiresIn = DefaultInviteExpiry
	}
	if in.ExpiresIn < time.Minute || in.ExpiresIn > MaxInviteExpiry {
		fields["expires_in"] = "must be between 1 minute and 90 days"
	}
	if in.MaxUses == 0 {
		in.MaxUses = 1
	}
	if in.MaxUses < 1 || in.MaxUses > MaxInviteUses {
		fields["max_uses"] = fmt.Sprintf("must be between 1 and %d", MaxInviteUses)
	}
	note, err := normalizeNote(in.Note)
	if err != nil {
		var ve *ValidationError
		if errors.As(err, &ve) {
			for k, v := range ve.Fields {
				fields[k] = v
			}
		}
	}
	if len(fields) > 0 {
		return nil, "", &ValidationError{Fields: fields}
	}
	code, err := NewToken()
	if err != nil {
		return nil, "", err
	}
	inv := &Invite{Role: in.Role, Note: note, MaxUses: in.MaxUses, CreatedBy: actor.ID}
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		inv.ID = tx.NewID()
		inv.CreatedAt = tx.Now()
		inv.ExpiresAt = inv.CreatedAt + in.ExpiresIn.Milliseconds()
		var createdBy any
		if actor.ID != "" {
			createdBy = actor.ID
		}
		if _, err := tx.Exec(`INSERT INTO invites (id, code_hash, created_by, role, note, expires_at, max_uses, use_count, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			inv.ID, HashToken(code), createdBy, string(inv.Role), inv.Note, inv.ExpiresAt, inv.MaxUses, inv.CreatedAt); err != nil {
			return fmt.Errorf("insert invite: %w", err)
		}
		_, err := audit.Write(tx, actor.Entry(audit.ActionInviteCreate, "invite", inv.ID, map[string]any{
			"role": inv.Role, "max_uses": inv.MaxUses, "expires_at": inv.ExpiresAt,
		}))
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return inv, code, nil
}

// InviteStatus describes what an invite code currently allows.
type InviteStatus struct {
	Valid bool
	Role  Role
}

// LookupInvite reports whether code is a usable invite (known, not revoked,
// not expired, not used up) and the role it grants.
func (s *Service) LookupInvite(ctx context.Context, code string) (InviteStatus, error) {
	if code == "" || len(code) > 128 {
		return InviteStatus{}, nil
	}
	var role string
	now := s.now().UnixMilli()
	err := s.db.Read().QueryRowContext(ctx, `SELECT role FROM invites
		WHERE code_hash = ? AND revoked_at IS NULL AND expires_at > ? AND use_count < max_uses`,
		HashToken(code), now).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return InviteStatus{}, nil
	}
	if err != nil {
		return InviteStatus{}, fmt.Errorf("look up invite: %w", err)
	}
	return InviteStatus{Valid: true, Role: Role(role)}, nil
}

// AcceptInviteInput is the input of AcceptInvite.
type AcceptInviteInput struct {
	Code string
	// Name is the person's display name; when set, the handle is derived
	// from it and Username / DisplayName are ignored.
	Name        string
	Username    string
	Password    string
	DisplayName string
	UserAgent   string
	IP          string
}

// AcceptInvite redeems one use of an invite: it creates the account with the
// invite's role and starts a session, in one transaction. A bad code is
// ErrInviteInvalid; a taken username is ErrUsernameTaken (and consumes no
// use).
func (s *Service) AcceptInvite(ctx context.Context, in AcceptInviteInput) (*SessionResult, error) {
	st, err := s.LookupInvite(ctx, in.Code)
	if err != nil {
		return nil, err
	}
	if !st.Valid {
		return nil, ErrInviteInvalid
	}
	f, verr := validateSignup(in.Name, in.Username, in.DisplayName, in.Password)
	if verr != nil {
		return nil, verr
	}
	pwHash, err := s.hasher.Hash(ctx, f.password)
	if err != nil {
		return nil, err
	}
	var res SessionResult
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		var id, role string
		err := tx.QueryRow(`UPDATE invites SET use_count = use_count + 1
			WHERE code_hash = ? AND revoked_at IS NULL AND expires_at > ? AND use_count < max_uses
			RETURNING id, role`, HashToken(in.Code), tx.Now()).Scan(&id, &role)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInviteInvalid
		}
		if err != nil {
			return fmt.Errorf("use invite: %w", err)
		}
		user, err := insertUser(tx, f, Role(role), pwHash)
		if err != nil {
			return err
		}
		if err := s.userCreated(ctx, tx, user.ID); err != nil {
			return err
		}
		token, sess, err := s.createSession(tx, user.ID, SessionWeb, in.UserAgent, in.IP)
		if err != nil {
			return err
		}
		res = SessionResult{Token: token, User: user, Session: sess}
		_, err = audit.Write(tx, audit.User(user.ID, in.IP).Entry(audit.ActionInviteAccept, "invite", id,
			map[string]string{"username": user.Username, "role": role}))
		return err
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// RevokeInvite makes an invite unusable. Revoking twice keeps the first
// revocation time; an unknown id is ErrNotFound.
func (s *Service) RevokeInvite(ctx context.Context, actor audit.Actor, id string) error {
	return s.db.Do(ctx, func(tx *store.Tx) error {
		var revoked sql.NullInt64
		err := tx.QueryRow(`SELECT revoked_at FROM invites WHERE id = ?`, id).Scan(&revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("find invite: %w", err)
		}
		if revoked.Valid {
			return nil
		}
		if _, err := tx.Exec(`UPDATE invites SET revoked_at = ? WHERE id = ?`, tx.Now(), id); err != nil {
			return fmt.Errorf("revoke invite: %w", err)
		}
		_, err = audit.Write(tx, actor.Entry(audit.ActionInviteRevoke, "invite", id, nil))
		return err
	})
}

// ListInvites returns every invite, newest first.
func (s *Service) ListInvites(ctx context.Context) ([]Invite, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT id, COALESCE(created_by, ''), role, note, expires_at, max_uses, use_count, revoked_at, created_at
		FROM invites ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var inv Invite
		var role string
		var revoked sql.NullInt64
		if err := rows.Scan(&inv.ID, &inv.CreatedBy, &role, &inv.Note, &inv.ExpiresAt, &inv.MaxUses, &inv.UseCount, &revoked, &inv.CreatedAt); err != nil {
			return nil, fmt.Errorf("list invites: %w", err)
		}
		inv.Role = Role(role)
		if revoked.Valid {
			inv.RevokedAt = &revoked.Int64
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// RegisterInput is the input of Register.
type RegisterInput struct {
	// Name is the person's display name; the handle is derived from it.
	Name      string
	Password  string
	UserAgent string
	IP        string
}

// Register creates a member account while sign-up is open (switched on and
// inside its window; ErrRegistrationClosed otherwise) and starts a session.
// Every attempt counts against the per-IP limit.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*SessionResult, error) {
	if err := s.signupAllowed(in.IP); err != nil {
		return nil, err
	}
	f, verr := validateNewName(in.Name, in.Password)
	if verr != nil {
		return nil, verr
	}
	pwHash, err := s.hasher.Hash(ctx, f.password)
	if err != nil {
		return nil, err
	}
	var res SessionResult
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		user, err := insertUser(tx, f, RoleMember, pwHash)
		if err != nil {
			return err
		}
		if err := s.userCreated(ctx, tx, user.ID); err != nil {
			return err
		}
		token, sess, err := s.createSession(tx, user.ID, SessionWeb, in.UserAgent, in.IP)
		if err != nil {
			return err
		}
		res = SessionResult{Token: token, User: user, Session: sess}
		_, err = audit.Write(tx, audit.User(user.ID, in.IP).Entry(audit.ActionRegister, "user", user.ID,
			map[string]string{"username": user.Username}))
		return err
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// signupAllowed gates the public sign-up endpoints: the per-IP attempt limit
// (every attempt counts, successful or not), then the sign-up window.
func (s *Service) signupAllowed(ip string) error {
	if key := IPKey(ip); key != "" {
		if wait, blocked := s.signups.Blocked(key); blocked {
			return &RateLimitedError{RetryAfter: wait}
		}
		s.signups.Fail(key)
	}
	if !s.settings.RegistrationOpen(s.now()) {
		return ErrRegistrationClosed
	}
	return nil
}
