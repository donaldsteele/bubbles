package protocol

import "encoding/json"

// Event is the envelope of one realtime event (WebSocket frame and replay).
// Persisted events carry their global Seq; ephemeral frames use seq 0.
//
// Data holds one of the payload types below, selected by Type:
//
//	message.created, message.updated  Message
//	message.deleted                   MessageDeleted
//	reaction.added, reaction.removed  ReactionEvent
//	pin.added, pin.removed            PinEvent
//	channel.created, .updated,
//	channel.archived, .unarchived     ChannelEvent
//	channel.member_joined             MemberJoinedEvent
//	channel.member_left               MemberLeftEvent
//	membership.updated                Membership (user-scoped)
//	read.updated                      ReadEvent (user-scoped)
//	alert.updated                     AlertUpdatedEvent
//
// Channel-scoped events carry ChannelID and are delivered to the channel's
// members (public channel lifecycle events to every non-guest); user-scoped
// events carry no ChannelID and go to the one user named in the payload.
type Event struct {
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	ChannelID string          `json:"channel_id,omitempty"`
	TS        string          `json:"ts"`
	Data      json.RawMessage `json:"data"`
}

// MessageDeleted is the payload of message.deleted. The message row stays as
// a tombstone; ThreadRootID is set when the deleted message was a reply.
type MessageDeleted struct {
	ID           string  `json:"id"`
	ChannelID    string  `json:"channel_id"`
	ThreadRootID *string `json:"thread_root_id"`
	DeletedAt    string  `json:"deleted_at"`
}

// ReactionEvent is the payload of reaction.added and reaction.removed. Count
// is the number of users with this emoji on the message after the change.
type ReactionEvent struct {
	MessageID string `json:"message_id"`
	ChannelID string `json:"channel_id"`
	UserID    string `json:"user_id"`
	Emoji     string `json:"emoji"`
	Count     int    `json:"count"`
}

// PinEvent is the payload of pin.added and pin.removed.
type PinEvent struct {
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
	UserID    string `json:"user_id"`
	PinnedAt  string `json:"pinned_at,omitempty"`
}

// ChannelEvent is the payload of channel.created, channel.updated,
// channel.archived and channel.unarchived. Memberships lists the initial
// members on channel.created only.
type ChannelEvent struct {
	Channel     Channel      `json:"channel"`
	Memberships []Membership `json:"memberships,omitempty"`
}

// MemberJoinedEvent is the payload of channel.member_joined. Channel and
// Membership let the new member's client add the channel without a refetch.
type MemberJoinedEvent struct {
	ChannelID  string     `json:"channel_id"`
	UserID     string     `json:"user_id"`
	ActorID    string     `json:"actor_id"`
	Channel    Channel    `json:"channel"`
	Membership Membership `json:"membership"`
}

// MemberLeftEvent is the payload of channel.member_left. ActorID differs from
// UserID when the member was removed. The departing user must still receive
// the event although they are no longer a member.
type MemberLeftEvent struct {
	ChannelID string `json:"channel_id"`
	UserID    string `json:"user_id"`
	ActorID   string `json:"actor_id"`
}

// ReadEvent is the payload of read.updated: the new read marker of the
// recipient and the unread and mention counts after the change.
type ReadEvent struct {
	ChannelID  string `json:"channel_id"`
	LastReadID string `json:"last_read_id"`
	Unread     int    `json:"unread"`
	Mentions   int    `json:"mentions"`
}

// Alert transitions: what an alert event did to its alert. They are the values
// of AlertUpdatedEvent.Transition; a new alert message (message.created, kind
// alert) is implicitly TransitionCreated. Only created, escalated and
// renotify_due can notify (pkg/notifyrules); the others are informational.
const (
	AlertTransitionCreated      = "created"
	AlertTransitionEscalated    = "escalated"
	AlertTransitionRenotifyDue  = "renotify_due"
	AlertTransitionUpdatedOther = "updated_other"
	AlertTransitionAcked        = "acked"
	AlertTransitionResolved     = "resolved"
)

// AlertUpdatedEvent is the payload of alert.updated: the alert's message as
// it stands after the update and the transition that caused the event. The
// notification dispatcher and the clients decide notifications from
// Transition and Message.Alert.Severity.
type AlertUpdatedEvent struct {
	Message    Message `json:"message"`
	Transition string  `json:"transition"`
}
