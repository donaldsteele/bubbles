package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// VerifyPassword re-checks the password of an active account before a
// destructive action (reset). A wrong password, an unknown or deactivated
// account and an account without a password are all ErrWrongPassword; each
// costs one full argon2id comparison.
func (s *Service) VerifyPassword(ctx context.Context, userID, password string) error {
	var pwHash string
	err := s.db.Read().QueryRowContext(ctx, `SELECT COALESCE(pw_hash, '') FROM users WHERE id = ? AND deactivated_at IS NULL`, userID).Scan(&pwHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load password: %w", err)
	}
	if pwHash == "" {
		s.verifyDummy(ctx, password)
		return ErrWrongPassword
	}
	ok, err := s.verify(ctx, password, pwHash)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if !ok {
		return ErrWrongPassword
	}
	return nil
}
