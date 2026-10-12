package protocol

// Channel kinds.
const (
	ChannelPublic  = "public"
	ChannelPrivate = "private"
	ChannelDM      = "dm"
	ChannelGroupDM = "group_dm"
)

// Membership roles.
const (
	MemberOwner  = "owner"
	MemberMember = "member"
)

// Notification levels of a membership.
const (
	NotifyAll      = "all"
	NotifyMentions = "mentions"
	NotifyNone     = "none"
)

// Alert severities.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Channel is the wire view of a channel row. Name is null for direct
// messages; ParticipantIDs is set for dm and group_dm only (a note-to-self
// has exactly one participant).
type Channel struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Name           *string  `json:"name"`
	Topic          string   `json:"topic"`
	Description    string   `json:"description"`
	IsDefault      bool     `json:"is_default"`
	RetentionDays  *int     `json:"retention_days"`
	CreatedBy      *string  `json:"created_by"`
	CreatedAt      string   `json:"created_at"`
	ArchivedAt     *string  `json:"archived_at"`
	MemberCount    int      `json:"member_count"`
	ParticipantIDs []string `json:"participant_ids,omitempty"`
}

// Membership is one user's own relation to a channel, including private
// preferences. LastReadID and MutedUntil are null when unset.
type Membership struct {
	ChannelID        string  `json:"channel_id"`
	UserID           string  `json:"user_id"`
	Role             string  `json:"role"`
	JoinedAt         string  `json:"joined_at"`
	LastReadID       *string `json:"last_read_id"`
	NotifyLevel      string  `json:"notify_level"`
	AlertMinSeverity string  `json:"alert_min_severity"`
	MutedUntil       *string `json:"muted_until"`
}

// Member is the public view of a channel member (GET /channels/{id}/members).
type Member struct {
	UserID   string `json:"user_id"`
	Role     string `json:"role"`
	JoinedAt string `json:"joined_at"`
}

// Unread is the unread and mention count of one joined channel.
type Unread struct {
	ChannelID string `json:"channel_id"`
	Unread    int    `json:"unread"`
	Mentions  int    `json:"mentions"`
}

// SettingsPublic is the part of the instance settings every signed-in user
// may read.
type SettingsPublic struct {
	InstanceName   string `json:"instance_name"`
	UploadMaxBytes int64  `json:"upload_max_bytes"`
	WebPushEnabled bool   `json:"web_push_enabled"`
}

// Bootstrap is the body of GET /bootstrap. Everything in it is a consistent
// snapshot taken at SeqHead: a client replays events with seq > SeqHead.
type Bootstrap struct {
	InstanceID     string         `json:"instance_id"`
	Epoch          int64          `json:"epoch"`
	Me             Me             `json:"me"`
	Users          []User         `json:"users"`
	Channels       []Channel      `json:"channels"`
	Memberships    []Membership   `json:"memberships"`
	Unread         []Unread       `json:"unread"`
	SettingsPublic SettingsPublic `json:"settings_public"`
	SeqHead        int64          `json:"seq_head"`
}
