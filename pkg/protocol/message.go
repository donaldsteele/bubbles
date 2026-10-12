package protocol

// Message kinds.
const (
	MessageText   = "text"
	MessageAlert  = "alert"
	MessageSystem = "system"
)

// Alert states.
const (
	AlertOpen     = "open"
	AlertAcked    = "acked"
	AlertResolved = "resolved"
)

// Author is the sender of a message. Accounts are deactivated, never
// deleted, so the author is always present.
type Author struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
	Kind        string  `json:"kind"`
	AvatarURL   *string `json:"avatar_url"`
}

// Reaction is the aggregate of one emoji on a message.
type Reaction struct {
	Emoji   string   `json:"emoji"`
	Count   int      `json:"count"`
	UserIDs []string `json:"user_ids"`
}

// Mentions lists who a message mentions.
type Mentions struct {
	UserIDs []string `json:"user_ids"`
	Channel bool     `json:"channel"`
	Here    bool     `json:"here"`
}

// AlertField is one name/value pair of an alert.
type AlertField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// Alert is the alert state of a kind=alert message.
type Alert struct {
	Severity    string       `json:"severity"`
	State       string       `json:"state"`
	Title       string       `json:"title"`
	Fields      []AlertField `json:"fields"`
	Link        *string      `json:"link"`
	DedupeKey   string       `json:"dedupe_key"`
	Occurrences int          `json:"occurrences"`
	FirstSeenAt string       `json:"first_seen_at"`
	LastSeenAt  string       `json:"last_seen_at"`
	AckedBy     *string      `json:"acked_by"`
	AckedAt     *string      `json:"acked_at"`
	ResolvedBy  *string      `json:"resolved_by"`
	ResolvedAt  *string      `json:"resolved_at"`
}

// Message is the wire view of a chat message. A deleted message keeps its
// row as a tombstone: Deleted is true, Body is empty and Files, Reactions and
// Mentions are empty.
type Message struct {
	ID           string     `json:"id"`
	ChannelID    string     `json:"channel_id"`
	ThreadRootID *string    `json:"thread_root_id"`
	Kind         string     `json:"kind"`
	Author       Author     `json:"author"`
	OverrideName *string    `json:"override_name"`
	Body         string     `json:"body"`
	Files        []File     `json:"files"`
	Reactions    []Reaction `json:"reactions"`
	Mentions     Mentions   `json:"mentions"`
	ReplyCount   int        `json:"reply_count"`
	LastReplyAt  *string    `json:"last_reply_at"`
	EditedAt     *string    `json:"edited_at"`
	Deleted      bool       `json:"deleted"`
	CreatedAt    string     `json:"created_at"`
	Alert        *Alert     `json:"alert"`
}

// MessagePage is a page of messages in chronological order (oldest first).
type MessagePage struct {
	Items         []Message `json:"items"`
	HasMoreBefore bool      `json:"has_more_before"`
	HasMoreAfter  bool      `json:"has_more_after"`
}

// Thread is a thread root with a page of its replies, oldest first.
type Thread struct {
	Root          Message   `json:"root"`
	Replies       []Message `json:"replies"`
	HasMoreBefore bool      `json:"has_more_before"`
	HasMoreAfter  bool      `json:"has_more_after"`
}

// Pin is a pinned message of a channel.
type Pin struct {
	Message  Message `json:"message"`
	PinnedBy *string `json:"pinned_by"`
	PinnedAt string  `json:"pinned_at"`
}
