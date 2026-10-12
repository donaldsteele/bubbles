package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// MaxUserQueryChars bounds the q parameter of a user search.
const MaxUserQueryChars = 64

// ListUsers returns the accounts the actor may know about, ordered by id.
// Members and admins see everyone; guests and apps see only themselves and the
// users who share a channel with them (an app token's channel allowlist
// narrows that further). A non-empty q keeps users whose username or display
// name contains it, ignoring ASCII case.
func (s *Service) ListUsers(ctx context.Context, actor Actor, q string) ([]protocol.User, error) {
	if actor.Class() == "" {
		return nil, forbidden("unknown account role")
	}
	if err := requireDirectoryScope(actor); err != nil {
		return nil, err
	}
	q = strings.TrimSpace(q)
	if chars(q) > MaxUserQueryChars || !validText(q) {
		return nil, invalid("q", fmt.Sprintf("q must be at most %d characters", MaxUserQueryChars))
	}
	filter, args := "", []any(nil)
	if q != "" {
		like := "%" + likeEscaper.Replace(q) + "%"
		filter = `(username LIKE ? ESCAPE '\' OR display_name LIKE ? ESCAPE '\')`
		args = []any{like, like}
	}
	return visibleUsers(ctx, s.db.Read(), actor, restrictedView(actor), filter, args)
}

// GetUser returns one account the actor may know about; any other id,
// existing or not, is not found.
func (s *Service) GetUser(ctx context.Context, actor Actor, userID string) (protocol.User, error) {
	if actor.Class() == "" {
		return protocol.User{}, forbidden("unknown account role")
	}
	if err := requireDirectoryScope(actor); err != nil {
		return protocol.User{}, err
	}
	users, err := visibleUsers(ctx, s.db.Read(), actor, restrictedView(actor), `id = ?`, []any{userID})
	if err != nil {
		return protocol.User{}, err
	}
	if len(users) == 0 {
		return protocol.User{}, notFound()
	}
	return users[0], nil
}

// restrictedView reports whether the actor sees only the users it shares a
// channel with.
func restrictedView(a Actor) bool { return a.Class() == ClassGuest || a.IsApp() }

// likeEscaper makes the query literal under ESCAPE '\'. strings.Replacer scans
// the input once, so the escape character is doubled without being re-escaped.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
