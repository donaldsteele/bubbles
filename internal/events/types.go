// Package events defines the realtime event vocabulary and appends events to
// the global event log inside a store write transaction.
package events

import "encoding/json"

// Event types (docs/protocol.md). Persisted events carry a seq; ephemeral
// frames (typing, presence) are sent with seq 0 and never appended.
const (
	TypeMessageCreated = "message.created"
	TypeMessageUpdated = "message.updated"
	TypeMessageDeleted = "message.deleted"

	TypeReactionAdded   = "reaction.added"
	TypeReactionRemoved = "reaction.removed"

	TypePinAdded   = "pin.added"
	TypePinRemoved = "pin.removed"

	TypeChannelCreated      = "channel.created"
	TypeChannelUpdated      = "channel.updated"
	TypeChannelArchived     = "channel.archived"
	TypeChannelUnarchived   = "channel.unarchived"
	TypeChannelMemberJoined = "channel.member_joined"
	TypeChannelMemberLeft   = "channel.member_left"

	TypeMembershipUpdated = "membership.updated"
	TypeReadUpdated       = "read.updated"
	TypeUserUpdated       = "user.updated"
	TypePresenceChanged   = "presence.changed"
	TypeTyping            = "typing"
	TypeAlertUpdated      = "alert.updated"
	TypeCommandInvoked    = "command.invoked"
	TypeEphemeralMessage  = "ephemeral.message"
	TypeSettingsUpdated   = "settings.updated"
	TypeSessionRevoked    = "session.revoked"
)

// Types lists every event type.
var Types = []string{
	TypeMessageCreated, TypeMessageUpdated, TypeMessageDeleted,
	TypeReactionAdded, TypeReactionRemoved,
	TypePinAdded, TypePinRemoved,
	TypeChannelCreated, TypeChannelUpdated, TypeChannelArchived, TypeChannelUnarchived,
	TypeChannelMemberJoined, TypeChannelMemberLeft,
	TypeMembershipUpdated, TypeReadUpdated, TypeUserUpdated, TypePresenceChanged,
	TypeTyping, TypeAlertUpdated, TypeCommandInvoked, TypeEphemeralMessage,
	TypeSettingsUpdated, TypeSessionRevoked,
}

var known = func() map[string]bool {
	m := make(map[string]bool, len(Types))
	for _, t := range Types {
		m[t] = true
	}
	return m
}()

// Known reports whether t is a defined event type.
func Known(t string) bool { return known[t] }

// IsEphemeral reports whether t is sent as a seq-0 frame only and must never
// be appended to the event log.
func IsEphemeral(t string) bool {
	return t == TypeTyping || t == TypePresenceChanged || t == TypeEphemeralMessage
}

// WebSocket close codes sent by the server, with their reason strings.
const (
	CloseSlowConsumer   = 4008
	CloseSessionRevoked = 4009
	CloseServerReset    = 4010 // factory reset
	CloseContentCleared = 4011

	ReasonSlowConsumer   = "slow_consumer"
	ReasonSessionRevoked = "session_revoked"
	ReasonServerReset    = "server_reset"
	ReasonContentCleared = "content_cleared"
)

// Event is one row of the events table. Empty ChannelID / UserID are stored
// as NULL; UserID targets a user-scoped event at one user's connections.
type Event struct {
	Seq       int64
	Type      string
	ChannelID string
	UserID    string
	Payload   json.RawMessage
	CreatedAt int64 // unix ms
}
