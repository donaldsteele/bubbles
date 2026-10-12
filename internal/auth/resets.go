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

// ResetTTL is how long a password-reset link stays valid.
const ResetTTL = 24 * time.Hour

// CreateReset creates a one-time password-reset link for userID and returns
// its code and expiry. Earlier unused links of that user stop working. The
// code cannot be recovered afterwards.
func (s *Service) CreateReset(ctx context.Context, actor audit.Actor, userID string) (string, time.Time, error) {
	code, err := NewToken()
	if err != nil {
		return "", time.Time{}, err
	}
	var expires int64
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		var deactivated sql.NullInt64
		var kind string
		err := tx.QueryRow(`SELECT kind, deactivated_at FROM users WHERE id = ?`, userID).Scan(&kind, &deactivated)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("find user: %w", err)
		}
		if deactivated.Valid || kind != KindHuman {
			return ErrNotFound
		}
		now := tx.Now()
		expires = now + ResetTTL.Milliseconds()
		if _, err := tx.Exec(`UPDATE password_resets SET used_at = ? WHERE user_id = ? AND used_at IS NULL`, now, userID); err != nil {
			return fmt.Errorf("retire resets: %w", err)
		}
		var createdBy any
		if actor.ID != "" {
			createdBy = actor.ID
		}
		if _, err := tx.Exec(`INSERT INTO password_resets (code_hash, user_id, created_by, expires_at) VALUES (?, ?, ?, ?)`,
			HashToken(code), userID, createdBy, expires); err != nil {
			return fmt.Errorf("insert reset: %w", err)
		}
		_, err = audit.Write(tx, actor.Entry(audit.ActionResetCreate, "user", userID, nil))
		return err
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return code, time.UnixMilli(expires), nil
}

// ResetPassword sets a new password using a reset code. The code is single
// use. All of the user's sessions end and the user must sign in again. An
// unusable code is ErrResetInvalid; a weak password is a *ValidationError
// and leaves the code usable.
func (s *Service) ResetPassword(ctx context.Context, code, newPassword, ip string) error {
	if code == "" || len(code) > 128 {
		return ErrResetInvalid
	}
	if err := ValidatePassword(newPassword); err != nil {
		var pe *PolicyError
		if errors.As(err, &pe) {
			return &ValidationError{Fields: map[string]string{"password": pe.Reason}}
		}
		return err
	}
	var valid int
	err := s.db.Read().QueryRowContext(ctx, `SELECT 1 FROM password_resets
		WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?`, HashToken(code), s.now().UnixMilli()).Scan(&valid)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrResetInvalid
	}
	if err != nil {
		return fmt.Errorf("look up reset: %w", err)
	}
	pwHash, err := s.hasher.Hash(ctx, newPassword)
	if err != nil {
		return err
	}
	return s.db.Do(ctx, func(tx *store.Tx) error {
		var userID string
		err := tx.QueryRow(`UPDATE password_resets SET used_at = ?
			WHERE code_hash = ? AND used_at IS NULL AND expires_at > ? RETURNING user_id`,
			tx.Now(), HashToken(code), tx.Now()).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrResetInvalid
		}
		if err != nil {
			return fmt.Errorf("use reset: %w", err)
		}
		res, err := tx.Exec(`UPDATE users SET pw_hash = ?, must_change_pw = 0 WHERE id = ? AND deactivated_at IS NULL`, pwHash, userID)
		if err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrResetInvalid
		}
		if err := RevokeSessionsTx(tx, userID, nil); err != nil {
			return err
		}
		if err := RevokeDevicesTx(tx, userID); err != nil {
			return err
		}
		_, err = audit.Write(tx, audit.User(userID, ip).Entry(audit.ActionPasswordReset, "user", userID, nil))
		return err
	})
}

// ChangePasswordInput is the input of ChangePassword.
type ChangePasswordInput struct {
	UserID  string
	Current string
	New     string
	// KeepSession is the hash of the caller's own session, which survives.
	KeepSession []byte
	IP          string
}

// ChangePassword replaces a user's password after verifying the current one
// and revokes every other session of the user (and any pending reset link).
// A wrong current password is ErrWrongPassword and counts as a failed login
// for rate limiting; a weak new password is a *ValidationError.
func (s *Service) ChangePassword(ctx context.Context, in ChangePasswordInput) error {
	// Reserved before the check, as in Login: a wrong current password keeps
	// the reservation as a failure, a right one settles it below.
	attempt, wait, blocked := s.logins.Begin("pw:"+in.UserID, in.IP)
	if blocked {
		return &RateLimitedError{RetryAfter: wait}
	}
	var pwHash string
	err := s.db.Read().QueryRowContext(ctx, `SELECT COALESCE(pw_hash, '') FROM users WHERE id = ? AND deactivated_at IS NULL`, in.UserID).Scan(&pwHash)
	if errors.Is(err, sql.ErrNoRows) {
		attempt.Abort()
		return ErrNotFound
	}
	if err != nil {
		attempt.Abort()
		return fmt.Errorf("load password: %w", err)
	}
	ok := false
	if pwHash != "" {
		if ok, err = s.verify(ctx, in.Current, pwHash); err != nil && ctx.Err() != nil {
			attempt.Abort()
			return ctx.Err()
		}
	} else {
		s.verifyDummy(ctx, in.Current)
	}
	if !ok {
		return ErrWrongPassword
	}
	if err := ValidatePassword(in.New); err != nil {
		// The current password was right: the attempt is neither a failure
		// nor a success for the limiter.
		attempt.Abort()
		var pe *PolicyError
		if errors.As(err, &pe) {
			return &ValidationError{Fields: map[string]string{"new": pe.Reason}}
		}
		return err
	}
	attempt.Succeed()
	newHash, err := s.hasher.Hash(ctx, in.New)
	if err != nil {
		return err
	}
	return s.db.Do(ctx, func(tx *store.Tx) error {
		res, err := tx.Exec(`UPDATE users SET pw_hash = ?, must_change_pw = 0 WHERE id = ? AND deactivated_at IS NULL`, newHash, in.UserID)
		if err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(`UPDATE password_resets SET used_at = ? WHERE user_id = ? AND used_at IS NULL`, tx.Now(), in.UserID); err != nil {
			return fmt.Errorf("retire resets: %w", err)
		}
		if err := RevokeSessionsTx(tx, in.UserID, in.KeepSession); err != nil {
			return err
		}
		if err := RevokeDevicesTx(tx, in.UserID); err != nil {
			return err
		}
		_, err = audit.Write(tx, audit.User(in.UserID, in.IP).Entry(audit.ActionPasswordChange, "user", in.UserID, nil))
		return err
	})
}
