package chat

import (
	"errors"
	"slices"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Action is something an actor can try to do in or about a channel.
type Action string

// The actions of the permission matrix.
const (
	ActView          Action = "view"           // see the channel and read its history
	ActPost          Action = "post"           // send messages
	ActJoin          Action = "join"           // join a public channel
	ActLeave         Action = "leave"          // leave a public or private channel
	ActManage        Action = "manage"         // rename, topic, description, retention
	ActAddMember     Action = "add_member"     // add or promote a member
	ActRemoveMember  Action = "remove_member"  // remove another member
	ActDeleteAny     Action = "delete_any"     // delete other people's messages
	ActPin           Action = "pin"            // pin and unpin messages
	ActReact         Action = "react"          // add and remove own reactions
	ActArchive       Action = "archive"        // archive and unarchive
	ActCreateChannel Action = "create_channel" // create a public or private channel
	ActStartDM       Action = "start_dm"       // start a DM or group DM
)

// ActReadContent gates the bytes of a file, on top of ActView's visibility: an
// app needs messages:read for content, while channels:read shows the channel
// only. It is not a matrix column, so Actions does not list it.
const ActReadContent Action = "read_content"

// Actions lists every action in matrix order.
var Actions = []Action{
	ActView, ActPost, ActJoin, ActLeave, ActManage, ActAddMember, ActRemoveMember,
	ActDeleteAny, ActPin, ActReact, ActArchive, ActCreateChannel, ActStartDM,
}

// App token scopes the permission matrix looks at.
const (
	ScopeMessagesRead  = "messages:read"
	ScopeMessagesWrite = "messages:write"
	ScopeChannelsRead  = "channels:read"
	ScopeDMWrite       = "dm:write"
)

// Actor classes of the permission matrix.
const (
	ClassAdmin  = "admin"
	ClassMember = "member"
	ClassGuest  = "guest"
	ClassApp    = "app"
)

// Actor is the authenticated caller of a chat operation.
type Actor struct {
	ID string
	// Role is the account role: admin, member or guest.
	Role string
	// Kind is human (or empty) or app.
	Kind string
	// Scopes and ChannelIDs apply to apps only. ChannelIDs is the token's
	// channel allowlist; nil means every channel the app is a member of.
	Scopes     []string
	ChannelIDs []string
}

// IsApp reports whether the actor is an app account.
func (a Actor) IsApp() bool { return a.Kind == protocol.KindApp }

// Class returns the row of the permission matrix the actor falls in, or ""
// for an unknown role.
func (a Actor) Class() string {
	if a.IsApp() {
		return ClassApp
	}
	switch a.Role {
	case protocol.RoleAdmin:
		return ClassAdmin
	case protocol.RoleMember:
		return ClassMember
	case protocol.RoleGuest:
		return ClassGuest
	}
	return ""
}

// HasScope reports whether an app token carries scope.
func (a Actor) HasScope(scope string) bool { return slices.Contains(a.Scopes, scope) }

// requireDirectoryScope gates the account directory (bootstrap, user lookups)
// for apps: they need messages:read or channels:read. Humans are not checked.
func requireDirectoryScope(a Actor) error {
	if a.IsApp() && !a.HasScope(ScopeMessagesRead) && !a.HasScope(ScopeChannelsRead) {
		return forbidden("the token lacks the messages:read or channels:read scope")
	}
	return nil
}

// ChannelInfo is what the permission matrix needs to know about a channel and
// the actor's relation to it.
type ChannelInfo struct {
	ID       string
	Kind     string
	Archived bool
	// Member reports whether the actor is a member; Owner whether their
	// membership role is owner.
	Member bool
	Owner  bool
}

// Errors returned by the chat service. Callers match them with errors.Is; a
// *ValidationError additionally carries per-field reasons.
var (
	ErrNotFound   = errors.New("not found")
	ErrForbidden  = errors.New("forbidden")
	ErrConflict   = errors.New("conflict")
	ErrValidation = errors.New("validation failed")
)

// Error is a typed service error: Kind is one of the sentinels above and Msg
// is safe to show to the caller.
type Error struct {
	Kind error
	Msg  string
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Kind }

// ValidationError reports invalid input; Fields maps a request field to the
// reason it was rejected.
type ValidationError struct{ Fields map[string]string }

func (e *ValidationError) Error() string {
	for _, k := range sortedKeys(e.Fields) {
		return k + ": " + e.Fields[k]
	}
	return ErrValidation.Error()
}
func (e *ValidationError) Unwrap() error { return ErrValidation }

func notFound() error { return &Error{Kind: ErrNotFound, Msg: "not found"} }

func forbidden(msg string) error { return &Error{Kind: ErrForbidden, Msg: msg} }

func conflict(msg string) error { return &Error{Kind: ErrConflict, Msg: msg} }

func invalid(field, reason string) error {
	return &ValidationError{Fields: map[string]string{field: reason}}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Can decides whether actor may perform act on a channel. It returns nil when
// allowed, ErrNotFound when the channel is invisible to the actor (so its
// existence never leaks), ErrForbidden when it is visible but the action is
// not allowed, and ErrConflict when the channel is archived (read-only).
//
// Visibility: members see their channels; admins and members also see public
// channels; guests and apps see only channels they belong to (apps further
// limited by their token's channel allowlist). Nobody, admins included, sees
// a private channel, DM or group DM they are not in.
func Can(a Actor, act Action, ch ChannelInfo) error {
	class := a.Class()
	if class == "" {
		return forbidden("unknown account role")
	}
	switch act {
	case ActCreateChannel:
		if (class == ClassAdmin || class == ClassMember) && ch.Kind != protocol.ChannelDM && ch.Kind != protocol.ChannelGroupDM {
			return nil
		}
		return forbidden("you cannot create channels")
	case ActStartDM:
		if ch.Kind == protocol.ChannelPublic || ch.Kind == protocol.ChannelPrivate {
			return forbidden("not a direct message")
		}
		if class == ClassApp && !a.HasScope(ScopeDMWrite) {
			return forbidden("the token lacks the dm:write scope")
		}
		return nil
	}
	if !visible(a, class, ch) {
		return notFound()
	}
	switch act {
	case ActView:
		if class == ClassApp && !a.HasScope(ScopeMessagesRead) && !a.HasScope(ScopeChannelsRead) {
			return forbidden("the token lacks the messages:read scope")
		}
		return nil
	case ActReadContent:
		if class == ClassApp && !a.HasScope(ScopeMessagesRead) {
			return forbidden("the token lacks the messages:read scope")
		}
		return nil
	case ActPost, ActReact, ActPin:
		if class == ClassApp && !a.HasScope(ScopeMessagesWrite) {
			return forbidden("the token lacks the messages:write scope")
		}
		if !ch.Member {
			return forbidden("join the channel first")
		}
		return archivedCheck(ch)
	case ActJoin:
		switch {
		case class == ClassApp:
			return forbidden("apps are added to channels by an admin")
		case isDM(ch):
			return forbidden("direct messages have a fixed membership")
		case ch.Member:
			return nil
		}
		return archivedCheck(ch)
	case ActLeave:
		switch {
		case class == ClassApp:
			return forbidden("apps are removed from channels by an admin")
		case isDM(ch):
			return forbidden("direct messages have a fixed membership")
		case !ch.Member:
			return forbidden("you are not a member of this channel")
		}
		return nil
	case ActManage, ActAddMember, ActRemoveMember:
		if err := manager(class, ch); err != nil {
			return err
		}
		return archivedCheck(ch)
	case ActArchive:
		return manager(class, ch)
	case ActDeleteAny:
		if class == ClassApp || class == ClassGuest {
			return forbidden("you cannot delete other people's messages")
		}
		if !(ch.Member && ch.Owner && !isDM(ch)) && !(class == ClassAdmin && (ch.Member || ch.Kind == protocol.ChannelPublic)) {
			return forbidden("only the author, a channel owner or an admin can delete this message")
		}
		return archivedCheck(ch)
	}
	return forbidden("unknown action")
}

func isDM(ch ChannelInfo) bool {
	return ch.Kind == protocol.ChannelDM || ch.Kind == protocol.ChannelGroupDM
}

func visible(a Actor, class string, ch ChannelInfo) bool {
	if class == ClassApp {
		return ch.Member && (a.ChannelIDs == nil || slices.Contains(a.ChannelIDs, ch.ID))
	}
	if ch.Member {
		return true
	}
	return ch.Kind == protocol.ChannelPublic && (class == ClassAdmin || class == ClassMember)
}

// manager allows channel owners, and admins in public channels or channels
// they belong to. Direct messages have no managers; guests and apps never
// manage.
func manager(class string, ch ChannelInfo) error {
	if class == ClassApp || class == ClassGuest || isDM(ch) {
		return forbidden("you cannot manage this channel")
	}
	if ch.Member && ch.Owner {
		return nil
	}
	if class == ClassAdmin && (ch.Member || ch.Kind == protocol.ChannelPublic) {
		return nil
	}
	return forbidden("only a channel owner or an admin can do this")
}

func archivedCheck(ch ChannelInfo) error {
	if ch.Archived {
		return conflict("the channel is archived")
	}
	return nil
}
