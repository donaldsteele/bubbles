package apps

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

var commandName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// requireScope fails with forbidden unless the token carries scope.
func requireScope(g *auth.TokenGrant, scope string) error {
	if g == nil || !HasScope(g.Scopes, scope) {
		return forbidden("this token lacks the " + scope + " scope")
	}
	return nil
}

// SetCommands replaces the command set of the token's app atomically. Names
// are unique across all apps and must not shadow a built-in; a clash is a
// conflict that names the owner.
func (s *Service) SetCommands(ctx context.Context, g *auth.TokenGrant, in []protocol.CommandInput) ([]protocol.Command, error) {
	if err := requireScope(g, ScopeCommands); err != nil {
		return nil, err
	}
	if len(in) > protocol.MaxCommandsPerApp {
		return nil, invalid("commands", fmt.Sprintf("at most %d commands", protocol.MaxCommandsPerApp))
	}
	seen := map[string]bool{}
	for i, c := range in {
		field := fmt.Sprintf("commands[%d]", i)
		switch {
		case !commandName.MatchString(c.Name):
			return nil, invalid(field+".name", "name must be 1-32 characters: lowercase letters, digits or '-'")
		case seen[c.Name]:
			return nil, invalid(field+".name", "duplicate name")
		case utf8.RuneCountInString(c.Description) > protocol.MaxCommandTextChars || !validText(c.Description):
			return nil, invalid(field+".description", fmt.Sprintf("description must be valid text of at most %d characters", protocol.MaxCommandTextChars))
		case utf8.RuneCountInString(c.Usage) > protocol.MaxCommandTextChars || !validText(c.Usage):
			return nil, invalid(field+".usage", fmt.Sprintf("usage must be valid text of at most %d characters", protocol.MaxCommandTextChars))
		}
		seen[c.Name] = true
	}
	return run(ctx, s, func(tx *store.Tx) ([]protocol.Command, error) {
		ctx := tx.Context()
		var appName string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM apps WHERE id = ?`, g.AppID).Scan(&appName); err != nil {
			return nil, notFound()
		}
		if _, err := tx.Exec(`DELETE FROM commands WHERE app_id = ?`, g.AppID); err != nil {
			return nil, fmt.Errorf("clear commands: %w", err)
		}
		for _, c := range in {
			if chat.IsBuiltin(c.Name) {
				return nil, conflict("/" + c.Name + " is a built-in command")
			}
			var owner string
			err := tx.QueryRowContext(ctx, `SELECT a.name FROM commands c JOIN apps a ON a.id = c.app_id WHERE c.name = ?`, c.Name).Scan(&owner)
			if err == nil {
				return nil, conflict("/" + c.Name + " is already registered by " + owner)
			}
			if !isNoRows(err) {
				return nil, fmt.Errorf("check command: %w", err)
			}
			if _, err := tx.Exec(`INSERT INTO commands (id, app_id, name, description, usage) VALUES (?, ?, ?, ?, ?)`,
				tx.NewID(), g.AppID, c.Name, strings.TrimSpace(c.Description), strings.TrimSpace(c.Usage)); err != nil {
				return nil, fmt.Errorf("insert command: %w", err)
			}
		}
		out := make([]protocol.Command, 0, len(in))
		for _, c := range in {
			out = append(out, protocol.Command{
				Name: c.Name, Description: strings.TrimSpace(c.Description), Usage: strings.TrimSpace(c.Usage), AppName: appName,
			})
		}
		return out, nil
	})
}

// ListCommands returns the built-ins followed by the commands of every
// enabled app, by name.
func (s *Service) ListCommands(ctx context.Context) ([]protocol.Command, error) {
	out := chat.BuiltinCommands()
	rows, err := s.db.Read().QueryContext(ctx, `SELECT c.name, c.description, c.usage, a.name
		FROM commands c JOIN apps a ON a.id = c.app_id WHERE a.disabled_at IS NULL ORDER BY c.name`)
	if err != nil {
		return nil, fmt.Errorf("list commands: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c protocol.Command
		if err := rows.Scan(&c.Name, &c.Description, &c.Usage, &c.AppName); err != nil {
			return nil, fmt.Errorf("list commands: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
