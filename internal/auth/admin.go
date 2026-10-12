package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/store"
)

// Errors of the admin user operations; the HTTP layer answers 409.
var (
	// ErrLastAdmin means the change would leave no active admin.
	ErrLastAdmin = errors.New("at least one active admin account must remain")
	// ErrSelfLockout means an admin tried to demote or deactivate themselves.
	ErrSelfLockout = errors.New("you cannot demote or deactivate your own account")
)

// AdminUserPatch is a partial change of a human account by an admin.
type AdminUserPatch struct {
	Role        *Role
	Deactivated *bool
}

// AdminUpdateUser changes the role and/or the deactivated state of a human
// account. Deactivation ends every session, device token and app token of the
// user in the same transaction and appends user.updated {deactivated:true},
// which makes the realtime layer close the user's sockets. A role change
// appends user.updated too. Nothing changes if the patch is a no-op.
func (s *Service) AdminUpdateUser(ctx context.Context, actor audit.Actor, id string, p AdminUserPatch) (*User, error) {
	fields := map[string]string{}
	if p.Role != nil && !p.Role.Valid() {
		fields["role"] = "must be admin, member or guest"
	}
	if p.Role == nil && p.Deactivated == nil {
		fields["role"] = "give a role or deactivated"
	}
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}
	var out *User
	err := s.db.Do(ctx, func(tx *store.Tx) error {
		cur, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns("")+` FROM users WHERE id = ? AND kind = ?`, id, KindHuman))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load user: %w", err)
		}
		next := *cur
		if p.Role != nil {
			next.Role = *p.Role
		}
		if p.Deactivated != nil {
			if *p.Deactivated && next.DeactivatedAt == nil {
				now := tx.Now()
				next.DeactivatedAt = &now
			} else if !*p.Deactivated {
				next.DeactivatedAt = nil
			}
		}
		if next.Role == cur.Role && (next.DeactivatedAt == nil) == (cur.DeactivatedAt == nil) {
			out = cur
			return nil
		}
		losesAdmin := cur.Role == RoleAdmin && cur.DeactivatedAt == nil && (next.Role != RoleAdmin || next.DeactivatedAt != nil)
		if losesAdmin {
			if actor.ID == id {
				return ErrSelfLockout
			}
			var others int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND kind = 'human' AND deactivated_at IS NULL AND id != ?`, id).Scan(&others); err != nil {
				return fmt.Errorf("count admins: %w", err)
			}
			if others == 0 {
				return ErrLastAdmin
			}
		}
		var deact any
		if next.DeactivatedAt != nil {
			deact = *next.DeactivatedAt
		}
		if _, err := tx.Exec(`UPDATE users SET role = ?, deactivated_at = ? WHERE id = ?`, string(next.Role), deact, id); err != nil {
			return fmt.Errorf("update user: %w", err)
		}
		if next.DeactivatedAt != nil && cur.DeactivatedAt == nil {
			if err := RevokeSessionsTx(tx, id, nil); err != nil {
				return err
			}
			now := tx.Now()
			if _, err := tx.Exec(`UPDATE device_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, id); err != nil {
				return fmt.Errorf("revoke device tokens: %w", err)
			}
			if _, err := tx.Exec(`UPDATE app_tokens SET revoked_at = ? WHERE revoked_at IS NULL
				AND app_id IN (SELECT id FROM apps WHERE user_id = ?)`, now, id); err != nil {
				return fmt.Errorf("revoke app tokens: %w", err)
			}
		}
		payload, err := json.Marshal(publicUser(&next))
		if err != nil {
			return fmt.Errorf("encode user.updated: %w", err)
		}
		if _, err := events.Append(tx, events.Event{Type: events.TypeUserUpdated, Payload: payload}); err != nil {
			return err
		}
		detail := map[string]any{"role": next.Role, "deactivated": next.DeactivatedAt != nil}
		if _, err := audit.Write(tx, actor.Entry(audit.ActionUserUpdate, "user", id, detail)); err != nil {
			return err
		}
		out = &next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeAllSessions ends every session and device token of the human user
// userID (their open sockets close) and audits it.
func (s *Service) RevokeAllSessions(ctx context.Context, actor audit.Actor, userID string) error {
	return s.db.Do(ctx, func(tx *store.Tx) error {
		var one int
		err := tx.QueryRow(`SELECT 1 FROM users WHERE id = ? AND kind = ?`, userID, KindHuman).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("find user: %w", err)
		}
		if err := RevokeSessionsTx(tx, userID, nil); err != nil {
			return err
		}
		if err := RevokeDevicesTx(tx, userID); err != nil {
			return err
		}
		_, err = audit.Write(tx, actor.Entry(audit.ActionSessionRevoke, "user", userID, map[string]any{"all": true}))
		return err
	})
}
