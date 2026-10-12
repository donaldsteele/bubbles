package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/donaldsteele/bubbles/internal/store"
)

// maxHandleLen is the longest @handle (usernameRE allows 2 to 32).
const maxHandleLen = 32

// normalizeDisplayName trims s, collapses every run of Unicode whitespace to
// one ASCII space and checks the length and characters. It returns the
// cleaned name, or a reason it was rejected. An empty result is not an error
// here; callers that require a name check for it.
func normalizeDisplayName(s string) (string, string) {
	if !utf8.ValidString(s) {
		return "", "must be valid text"
	}
	s = strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
	if utf8.RuneCountInString(s) > maxDisplayNameLen {
		return "", fmt.Sprintf("must be at most %d characters", maxDisplayNameLen)
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", "must not contain control characters"
	}
	return s, ""
}

// displayKey is the folded form of a normalized display name that decides
// uniqueness and login: NFKC (so full-width letters match plain ones), then
// Unicode lower-casing.
func displayKey(name string) string {
	return strings.ToLower(norm.NFKC.String(name))
}

// handleBase derives an @handle candidate from a display name: accents are
// stripped, anything outside [a-z0-9._-] becomes '-', and the result is cut
// to 32 bytes. Names with nothing usable give "user".
func handleBase(name string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(name) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		r = unicode.ToLower(r)
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	h := trimHandle(collapseDashes(b.String()))
	if len(h) > maxHandleLen {
		h = trimHandle(h[:maxHandleLen])
	}
	if len(h) < 2 {
		return "user"
	}
	return h
}

func collapseDashes(s string) string {
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

func trimHandle(s string) string { return strings.Trim(s, "-._") }

// uniqueHandle returns base, or base-2, base-3 ... when taken, inside tx.
func uniqueHandle(tx *store.Tx, base string) (string, error) {
	for n := 1; n <= 1000; n++ {
		cand := base
		if n > 1 {
			suffix := fmt.Sprintf("-%d", n)
			stem := base
			if len(stem)+len(suffix) > maxHandleLen {
				stem = trimHandle(stem[:maxHandleLen-len(suffix)])
			}
			cand = stem + suffix
		}
		var one int
		err := tx.QueryRow(`SELECT 1 FROM users WHERE username = ?`, cand).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return cand, nil
		}
		if err != nil {
			return "", fmt.Errorf("check handle: %w", err)
		}
	}
	return "", errors.New("no free handle for " + base)
}

// nameTaken reports whether another active human already uses key.
func nameTaken(tx *store.Tx, key, exceptID string) (bool, error) {
	var one int
	err := tx.QueryRow(`SELECT 1 FROM users WHERE display_key = ? AND kind = 'human' AND deactivated_at IS NULL AND id != ?`,
		key, exceptID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check name: %w", err)
	}
	return true, nil
}

// validateNewName checks the fields of a name-first signup: the handle is
// derived later, inside the write transaction.
func validateNewName(name, password string) (newUserFields, *ValidationError) {
	fields := map[string]string{}
	f := newUserFields{password: password, uniqueName: true}
	n, why := normalizeDisplayName(name)
	switch {
	case why != "":
		fields["name"] = why
	case n == "":
		fields["name"] = "is required"
	}
	f.displayName = n
	f.displayKey = displayKey(n)
	if why := passwordField(password); why != "" {
		fields["password"] = why
	}
	if len(fields) > 0 {
		return f, &ValidationError{Fields: fields}
	}
	return f, nil
}

func passwordField(password string) string {
	if err := ValidatePassword(password); err != nil {
		var pe *PolicyError
		if errors.As(err, &pe) {
			return pe.Reason
		}
	}
	return ""
}

// backfillDisplayKeys fills display_key for human rows that lack it. It is
// idempotent and tolerates duplicate names (the key is not unique).
func (s *Service) backfillDisplayKeys(ctx context.Context) error {
	return s.db.Do(ctx, func(tx *store.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, display_name, username FROM users WHERE kind = 'human' AND display_key IS NULL`)
		if err != nil {
			return fmt.Errorf("scan display keys: %w", err)
		}
		type row struct{ id, key string }
		var todo []row
		for rows.Next() {
			var id, name, username string
			if err := rows.Scan(&id, &name, &username); err != nil {
				rows.Close()
				return fmt.Errorf("scan display keys: %w", err)
			}
			if name = strings.Join(strings.Fields(name), " "); name == "" {
				name = username
			}
			todo = append(todo, row{id, displayKey(name)})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("scan display keys: %w", err)
		}
		for _, r := range todo {
			if _, err := tx.Exec(`UPDATE users SET display_key = ? WHERE id = ?`, r.key, r.id); err != nil {
				return fmt.Errorf("backfill display key: %w", err)
			}
		}
		return nil
	})
}
