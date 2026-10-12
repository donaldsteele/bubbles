package apps

import (
	"fmt"
	"slices"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// The scopes an app token can carry (see protocol.AppScopes).
const (
	ScopeMessagesRead  = protocol.ScopeMessagesRead
	ScopeMessagesWrite = protocol.ScopeMessagesWrite
	ScopeAlertsWrite   = protocol.ScopeAlertsWrite
	ScopeFilesWrite    = protocol.ScopeFilesWrite
	ScopeChannelsRead  = protocol.ScopeChannelsRead
	ScopeEventsRead    = protocol.ScopeEventsRead
	ScopeCommands      = protocol.ScopeCommands
	ScopeDMWrite       = protocol.ScopeDMWrite
	ScopeMCP           = protocol.ScopeMCP
)

// ValidScope reports whether s names a scope.
func ValidScope(s string) bool { return slices.Contains(protocol.AppScopes, s) }

// NormalizeScopes validates scopes and returns them without duplicates in
// the canonical order of protocol.AppScopes. Unknown scopes are a validation
// error on field.
func NormalizeScopes(field string, scopes []string) ([]string, error) {
	seen := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		if !ValidScope(s) {
			return nil, invalid(field, fmt.Sprintf("unknown scope %q", truncate(s, 40)))
		}
		seen[s] = true
	}
	out := make([]string, 0, len(seen))
	for _, s := range protocol.AppScopes {
		if seen[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

// HasScope reports whether scopes contains scope.
func HasScope(scopes []string, scope string) bool { return slices.Contains(scopes, scope) }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
