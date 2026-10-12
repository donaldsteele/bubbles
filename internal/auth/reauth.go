package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// AdoptSetupToken is InitSetup for a database a factory reset has just
// recreated: with no users it keeps the well-formed token already in
// setup-token.txt (the one the reset handed to the admin or printed on the
// console) instead of replacing it, and creates one only when the file is
// missing or malformed. With users it behaves like InitSetup.
func (s *Service) AdoptSetupToken(ctx context.Context) (string, error) {
	needs, err := s.NeedsSetup(ctx)
	if err != nil {
		return "", err
	}
	if !needs || s.dataDir == "" {
		return s.InitSetup(ctx)
	}
	b, err := os.ReadFile(filepath.Join(s.dataDir, SetupTokenFile))
	if err != nil {
		return s.NewSetupToken()
	}
	token := strings.TrimSpace(string(b))
	if raw, derr := tokenEncoding.DecodeString(strings.ToUpper(token)); derr != nil || len(raw) != 32 || token != strings.ToLower(token) {
		return s.NewSetupToken()
	}
	s.setupMu.Lock()
	s.setupHash = HashToken(token)
	s.setupMu.Unlock()
	return token, nil
}
