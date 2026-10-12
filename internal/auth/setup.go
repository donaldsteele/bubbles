package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
)

// SetupTokenFile is the file in the data directory that holds the one-time
// setup token until setup completes.
const SetupTokenFile = "setup-token.txt"

// NeedsSetup reports whether no account exists yet.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	n, err := s.CountUsers(ctx)
	return n == 0, err
}

// InitSetup prepares first-run setup at server start. With no users it
// creates a setup token (see NewSetupToken) and returns it so the caller can
// print it on the console. With users it removes a stale token file and
// returns "".
func (s *Service) InitSetup(ctx context.Context) (string, error) {
	needs, err := s.NeedsSetup(ctx)
	if err != nil {
		return "", err
	}
	if !needs {
		s.clearSetupToken()
		return "", nil
	}
	return s.NewSetupToken()
}

// NewSetupToken replaces the setup token: 256 random bits in base32, written
// to <data>/setup-token.txt (0600). Only the SHA-256 of the token is kept in
// memory. Factory reset calls it to return to the setup wizard.
func (s *Service) NewSetupToken() (string, error) {
	if s.dataDir == "" {
		return "", errors.New("auth: no data directory for the setup token")
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	token, err := WriteSetupToken(s.dataDir)
	if err != nil {
		return "", err
	}
	s.setupHash = HashToken(token)
	return token, nil
}

// WriteSetupToken writes a new setup token to <dataDir>/setup-token.txt
// (0600), replacing any old one, and returns it. Factory reset calls it with
// the server's services torn down; the rebuilt service picks the token up
// with AdoptSetupToken.
func WriteSetupToken(dataDir string) (string, error) {
	token, err := NewToken()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, SetupTokenFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("replace %s: %w", SetupTokenFile, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, config.FileMode)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", SetupTokenFile, err)
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		f.Close()
		return "", fmt.Errorf("write %s: %w", SetupTokenFile, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("write %s: %w", SetupTokenFile, err)
	}
	return token, nil
}

// clearSetupToken forgets the token and deletes its file.
func (s *Service) clearSetupToken() {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	s.setupHash = nil
	if s.dataDir == "" {
		return
	}
	if err := os.Remove(filepath.Join(s.dataDir, SetupTokenFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Warn("remove setup token file", "err", err)
	}
}

// setupTokenValid compares token with the active one in constant time.
func (s *Service) setupTokenValid(token string) bool {
	s.setupMu.Lock()
	want := s.setupHash
	s.setupMu.Unlock()
	if want == nil {
		return false
	}
	got := HashToken(strings.ToLower(strings.TrimSpace(token)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// SetupInput is the input of Setup.
type SetupInput struct {
	Token        string
	Username     string
	Password     string
	DisplayName  string
	InstanceName string
	UserAgent    string
	IP           string
}

// Setup completes first-run setup: with the right token it creates the
// admin account, sets the instance name, runs the OnSetup hook and starts a
// session, all in one transaction, then deletes the token. It works exactly
// once: afterwards it returns ErrSetupDone, whatever the token. A missing or
// wrong token is ErrSetupToken (wrong tokens count against the caller's IP
// in the login rate limit).
func (s *Service) Setup(ctx context.Context, in SetupInput) (*SessionResult, error) {
	needs, err := s.NeedsSetup(ctx)
	if err != nil {
		return nil, err
	}
	if !needs {
		return nil, ErrSetupDone
	}
	// A wrong token keeps the reserved IP slot; a right one refunds it.
	attempt, wait, blocked := s.logins.Begin("", in.IP)
	if blocked {
		return nil, &RateLimitedError{RetryAfter: wait}
	}
	if !s.setupTokenValid(in.Token) {
		return nil, ErrSetupToken
	}
	attempt.Succeed()

	f, verr := validateNewUser(in.Username, in.DisplayName, in.Password)
	instance := strings.TrimSpace(in.InstanceName)
	if err := settings.Validate(settings.KeyInstanceName, instance); err != nil {
		if verr == nil {
			verr = &ValidationError{Fields: map[string]string{}}
		}
		verr.Fields["instance_name"] = strings.TrimPrefix(err.Error(), settings.ErrInvalid.Error()+": ")
	}
	if verr != nil {
		return nil, verr
	}
	pwHash, err := s.hasher.Hash(ctx, f.password)
	if err != nil {
		return nil, err
	}

	var res SessionResult
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			return fmt.Errorf("count users: %w", err)
		}
		if n > 0 {
			return ErrSetupDone
		}
		admin, err := insertUser(tx, f, RoleAdmin, pwHash)
		if err != nil {
			return err
		}
		actor := audit.User(admin.ID, in.IP)
		if err := s.settings.SetTx(tx, actor, settings.KeyInstanceName, instance); err != nil {
			return err
		}
		if s.onSetup != nil {
			if err := s.onSetup(ctx, tx, admin.ID); err != nil {
				return fmt.Errorf("setup hook: %w", err)
			}
		}
		if err := s.userCreated(ctx, tx, admin.ID); err != nil {
			return err
		}
		token, sess, err := s.createSession(tx, admin.ID, SessionWeb, in.UserAgent, in.IP)
		if err != nil {
			return err
		}
		res = SessionResult{Token: token, User: admin, Session: sess}
		_, err = audit.Write(tx, actor.Entry(audit.ActionSetup, "user", admin.ID, map[string]string{"username": admin.Username}))
		return err
	})
	if err != nil {
		return nil, err
	}
	s.clearSetupToken()
	return &res, nil
}
