package protocol

import (
	"bytes"
	"encoding/json"
)

// Patch is one field of a partial update: absent from the JSON (Set false,
// leave unchanged), null (Set true, Value nil: clear) or a value.
type Patch[T any] struct {
	Set   bool
	Value *T
}

// UnmarshalJSON records that the field was present.
func (p *Patch[T]) UnmarshalJSON(b []byte) error {
	p.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		p.Value = nil
		return nil
	}
	p.Value = new(T)
	return json.Unmarshal(b, p.Value)
}

// MarshalJSON writes the value, or null to clear it.
func (p Patch[T]) MarshalJSON() ([]byte, error) {
	if p.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(p.Value)
}

// IsZero lets `omitzero` drop an unset field.
func (p Patch[T]) IsZero() bool { return !p.Set }

// CreateChannelRequest is the body of POST /channels.
type CreateChannelRequest struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Topic     string   `json:"topic,omitempty"`
	MemberIDs []string `json:"member_ids,omitempty"`
}

// UpdateChannelRequest is the body of PATCH /channels/{id}. retention_days
// null removes the channel's own retention.
type UpdateChannelRequest struct {
	Name          *string    `json:"name,omitempty"`
	Topic         *string    `json:"topic,omitempty"`
	Description   *string    `json:"description,omitempty"`
	RetentionDays Patch[int] `json:"retention_days,omitzero"`
}

// AddMemberRequest is the optional body of PUT /channels/{id}/members/{user_id}.
type AddMemberRequest struct {
	Role string `json:"role,omitempty"`
}

// UpdateMembershipRequest is the body of PATCH /channels/{id}/membership.
// muted_until null unmutes.
type UpdateMembershipRequest struct {
	NotifyLevel      *string       `json:"notify_level,omitempty"`
	AlertMinSeverity *string       `json:"alert_min_severity,omitempty"`
	MutedUntil       Patch[string] `json:"muted_until,omitzero"`
}

// MarkReadRequest is the body of POST /channels/{id}/read.
type MarkReadRequest struct {
	MessageID string `json:"message_id"`
}

// StartDMRequest is the body of POST /dms.
type StartDMRequest struct {
	UserIDs []string `json:"user_ids"`
}

// CreateMessageRequest is the body of POST /channels/{id}/messages. A repeated
// nonce returns the message it created first.
type CreateMessageRequest struct {
	Body         string   `json:"body"`
	ThreadRootID *string  `json:"thread_root_id,omitempty"`
	FileIDs      []string `json:"file_ids,omitempty"`
	Nonce        string   `json:"nonce,omitempty"`
}

// EditMessageRequest is the body of PATCH /messages/{id}.
type EditMessageRequest struct {
	Body string `json:"body"`
}

// UserList is the body of GET /users.
type UserList struct {
	Items []User `json:"items"`
}

// ChannelList is the body of GET /channels.
type ChannelList struct {
	Items []Channel `json:"items"`
}

// MemberList is the body of GET /channels/{id}/members.
type MemberList struct {
	Items []Member `json:"items"`
}

// PinList is the body of GET /channels/{id}/pins, newest pin first.
type PinList struct {
	Items []Pin `json:"items"`
}

// ReactionList is the body of PUT and DELETE /messages/{id}/reactions/{emoji}:
// the message's reactions after the change.
type ReactionList struct {
	Items []Reaction `json:"items"`
}

// MaxPrefsBytes is the largest prefs object PATCH /me accepts.
const MaxPrefsBytes = 4096

// UpdateMeRequest is the body of PATCH /me. An absent field is left alone.
// display_name and prefs cannot be null; status_text null clears the text;
// avatar_file_id null removes the avatar. prefs replaces the whole object.
type UpdateMeRequest struct {
	DisplayName  Patch[string]          `json:"display_name,omitzero"`
	StatusText   Patch[string]          `json:"status_text,omitzero"`
	AvatarFileID Patch[string]          `json:"avatar_file_id,omitzero"`
	Prefs        Patch[json.RawMessage] `json:"prefs,omitzero"`
}
