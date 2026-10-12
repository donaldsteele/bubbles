package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
)

const (
	// AdminPasswordFile is the file in the data directory that holds the
	// first admin's generated password until that password changes.
	AdminPasswordFile = "admin-password.txt"
	// BootstrapAdminUsername is the username of the account BootstrapAdmin
	// creates.
	BootstrapAdminUsername = "admin"
)

// passwordAlphabet leaves out look-alikes (0/o, 1/l/i) so a password read
// off a screen is typed correctly.
const passwordAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// GeneratePassword returns a random password of 16 characters from
// passwordAlphabet, grouped 4-4-4-4 with dashes (19 characters in all).
func GeneratePassword() (string, error) {
	max := big.NewInt(int64(len(passwordAlphabet)))
	var b strings.Builder
	for i := 0; i < 16; i++ {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		b.WriteByte(passwordAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// BootstrapAdmin creates the first account on an empty database: user
// "admin" (display name "Admin", role admin) with a generated password. In
// one transaction it runs the OnBootstrap and OnUserCreated hooks and writes
// the setup.complete audit row (console actor); afterwards it writes the
// password to <data>/admin-password.txt (0600) and returns it. With any user
// present it does nothing and returns "".
func (s *Service) BootstrapAdmin(ctx context.Context) (string, error) {
	n, err := s.CountUsers(ctx)
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "", nil
	}
	password := s.adminPassword
	if password == "" {
		if password, err = GeneratePassword(); err != nil {
			return "", err
		}
	}
	pwHash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return "", err
	}
	f := newUserFields{
		username: BootstrapAdminUsername, displayName: "Admin", displayKey: displayKey("Admin"), password: password,
	}
	created := false
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		created = false
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			return fmt.Errorf("count users: %w", err)
		}
		if n > 0 {
			return nil
		}
		admin, err := insertUser(tx, f, RoleAdmin, pwHash)
		if err != nil {
			return err
		}
		if s.onBootstrap != nil {
			if err := s.onBootstrap(ctx, tx, admin.ID); err != nil {
				return fmt.Errorf("bootstrap hook: %w", err)
			}
		}
		if err := s.userCreated(ctx, tx, admin.ID); err != nil {
			return err
		}
		if _, err = audit.Write(tx, audit.Console.Entry(audit.ActionSetup, "user", admin.ID, map[string]string{"username": admin.Username})); err != nil {
			return err
		}
		// The sign-up window starts with the instance. Kept settings after a
		// factory reset may have sign-up off; that is left alone.
		if v := s.settings.Values(); v.OpenRegistration {
			until := settings.WindowEnd(s.now(), v.RegistrationWindowH)
			if err := s.settings.SetTx(tx, audit.Console, settings.KeyOpenRegistrationEnds, settings.DeadlineValue(until)); err != nil {
				return err
			}
		}
		created = true
		return nil
	})
	if err != nil {
		return "", err
	}
	if !created {
		return "", nil
	}
	if err := s.writeAdminPasswordFile(password); err != nil {
		return "", err
	}
	return password, nil
}

// writeAdminPasswordFile writes <data>/admin-password.txt (0600), replacing
// any stale one. It does nothing without a data directory.
func (s *Service) writeAdminPasswordFile(password string) error {
	if s.dataDir == "" {
		return nil
	}
	path := filepath.Join(s.dataDir, AdminPasswordFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("replace %s: %w", AdminPasswordFile, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, config.FileMode)
	if err != nil {
		return fmt.Errorf("create %s: %w", AdminPasswordFile, err)
	}
	body := "# bubbles admin sign-in. Deleted when the admin password changes.\n" +
		"username: " + BootstrapAdminUsername + "\n" +
		"password: " + password + "\n"
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", AdminPasswordFile, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", AdminPasswordFile, err)
	}
	return nil
}

// dropAdminPasswordFile deletes admin-password.txt after the password of
// username changed, when that user is the bootstrap admin.
func (s *Service) dropAdminPasswordFile(username string) {
	if s.dataDir == "" || username != BootstrapAdminUsername {
		return
	}
	if err := os.Remove(filepath.Join(s.dataDir, AdminPasswordFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Warn("remove admin password file", "err", err)
	}
}
