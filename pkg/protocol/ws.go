package protocol

// WebSocket endpoint and frames (docs/protocol.md). Server frames are the
// Hello greeting, Event envelopes (event.go) and Pong; client frames are
// ClientFrame values of at most MaxClientFrameBytes.

// WSPath is the WebSocket endpoint. Query parameters: QueryResume,
// QueryInstance, QueryEpoch.
const WSPath = "/api/v1/ws"

// WebSocket query parameters.
const (
	// QueryResume is the last seq the client applied (bootstrap seq_head or
	// the last event frame); the server replays retained events after it.
	QueryResume = "resume"
	// QueryInstance is the instance_id the client's state belongs to.
	QueryInstance = "instance"
	// QueryEpoch is the epoch the client's state belongs to.
	QueryEpoch = "epoch"
)

// Server frame types that are not events.
const (
	FrameHello = "hello"
	FramePong  = "pong"
)

// Hello.Resume outcomes.
const (
	// ResumeOK: the client's state is current; replayed events (if any)
	// follow, then live events.
	ResumeOK = "ok"
	// ResumeResyncRequired: the resume point is gone (retention, epoch
	// change); drop cached messages and drafts and re-fetch /bootstrap.
	ResumeResyncRequired = "resync_required"
	// ResumeInstanceChanged: the server was factory-reset or replaced; wipe
	// all local state.
	ResumeInstanceChanged = "instance_changed"
)

// Hello is the first frame on every connection.
type Hello struct {
	Type          string `json:"type"` // FrameHello
	InstanceID    string `json:"instance_id"`
	Epoch         int64  `json:"epoch"`
	SeqHead       int64  `json:"seq_head"`
	UserID        string `json:"user_id"`
	ServerVersion string `json:"server_version"`
	Resume        string `json:"resume"`
}

// Pong answers a client ping frame.
type Pong struct {
	Type string `json:"type"` // FramePong
}

// MaxClientFrameBytes bounds one client frame.
const MaxClientFrameBytes = 4096

// Client frame types.
const (
	ClientTyping   = "typing"
	ClientPresence = "presence"
	ClientFocus    = "focus"
	ClientPing     = "ping"
)

// Client presence reports (ClientFrame.State).
const (
	PresenceActive = "active"
	PresenceIdle   = "idle"
)

// Aggregated presence states (PresenceEvent.State). Idle doubles as the
// client report value.
const (
	PresenceOnline  = "online"
	PresenceOffline = "offline"
)

// ClientFrame is a client-to-server signal. Fields by type:
//
//	typing    channel_id (required)
//	presence  state: "active" | "idle"
//	focus     visible (required), channel_id (string or null), ts (optional RFC 3339)
//	ping      (none)
type ClientFrame struct {
	Type      string  `json:"type"`
	ChannelID *string `json:"channel_id,omitempty"`
	State     string  `json:"state,omitempty"`
	Visible   *bool   `json:"visible,omitempty"`
	TS        string  `json:"ts,omitempty"`
}

// WebSocket close codes (besides the standard 1000-1011).
const (
	CloseSlowConsumer   = 4008
	CloseSessionRevoked = 4009
	CloseServerReset    = 4010
	CloseContentCleared = 4011
)

// TypingEvent is the payload of the ephemeral typing event.
type TypingEvent struct {
	ChannelID string `json:"channel_id"`
	UserID    string `json:"user_id"`
}

// PresenceEvent is the payload of the ephemeral presence.changed event.
type PresenceEvent struct {
	UserID string `json:"user_id"`
	State  string `json:"state"` // PresenceOnline | PresenceIdle | PresenceOffline
}

// SessionRevokedEvent is the payload of session.revoked: one session
// (SessionID) or all of the user's sessions except ExceptSessionID.
type SessionRevokedEvent struct {
	SessionID       string `json:"session_id,omitempty"`
	All             bool   `json:"all,omitempty"`
	ExceptSessionID string `json:"except_session_id,omitempty"`
}

// SettingsUpdatedEvent is the payload of settings.updated: the changed
// setting keys; clients re-fetch what they display.
type SettingsUpdatedEvent struct {
	Keys []string `json:"keys"`
}
