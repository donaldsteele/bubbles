package protocol

import "encoding/json"

// AdminOverview is the body of GET /admin/overview.
type AdminOverview struct {
	InstanceName   string   `json:"instance_name"`
	Version        string   `json:"version"`
	StartedAt      string   `json:"started_at"`
	UptimeSeconds  int64    `json:"uptime_seconds"`
	Users          int      `json:"users"`
	Connections    int      `json:"connections"`
	StorageBytes   int64    `json:"storage_used_bytes"`
	DiskFreeBytes  *int64   `json:"disk_free_bytes"`
	TLSMode        string   `json:"tls_mode"`
	CAFingerprint  string   `json:"ca_fingerprint"`
	CertNotAfter   string   `json:"cert_not_after"`
	HTTPSURLs      []string `json:"https_urls"`
	OnboardingURLs []string `json:"onboarding_urls"`
	// ClientIsolationHint is true while no device other than the server
	// itself has signed in; Wi-Fi client isolation is a likely cause.
	ClientIsolationHint bool `json:"client_isolation_hint"`
}

// AdminSettings is the body of GET /admin/settings and the answer of PATCH:
// every setting with defaults applied.
type AdminSettings struct {
	InstanceName         string   `json:"instance_name"`
	OpenRegistration     bool     `json:"open_registration"`
	UploadMaxBytes       int64    `json:"upload_max_bytes"`
	UserQuotaBytes       int64    `json:"user_quota_bytes"`
	DiskReserveBytes     int64    `json:"disk_reserve_bytes"`
	DefaultRetentionDays *int     `json:"default_retention_days"`
	WebPushEnabled       bool     `json:"web_push_enabled"`
	PushPayload          string   `json:"push_payload"`
	AllowedCIDRs         []string `json:"allowed_cidrs"`
	SessionIdleDays      int      `json:"session_idle_days"`
	AlertRenotifyS       int      `json:"alert_renotify_s"`
	MDNSEnabled          bool     `json:"mdns_enabled"`
	WebResetEnabled      bool     `json:"web_reset_enabled"`
}

// AdminUserList is the body of GET /admin/users (human accounts).
type AdminUserList struct {
	Items []User `json:"items"`
}

// UpdateAdminUserRequest is the body of PATCH /admin/users/{id}.
type UpdateAdminUserRequest struct {
	Role        *string `json:"role,omitempty"`
	Deactivated *bool   `json:"deactivated,omitempty"`
}

// PasswordResetLink answers POST /admin/users/{id}/password-reset. URL is
// the one-time link; it is in this answer only.
type PasswordResetLink struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// AdminInvite is one invite; the code is never part of it.
type AdminInvite struct {
	ID        string  `json:"id"`
	Role      string  `json:"role"`
	Note      string  `json:"note"`
	CreatedBy *string `json:"created_by"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt string  `json:"expires_at"`
	MaxUses   int     `json:"max_uses"`
	UseCount  int     `json:"use_count"`
	RevokedAt *string `json:"revoked_at"`
}

// AdminInviteList is the body of GET /admin/invites.
type AdminInviteList struct {
	Items []AdminInvite `json:"items"`
}

// CreateInviteRequest is the body of POST /admin/invites. Zero fields take
// the defaults (member, 72 hours, one use).
type CreateInviteRequest struct {
	Role             string `json:"role,omitempty"`
	Note             string `json:"note,omitempty"`
	ExpiresInSeconds int    `json:"expires_in_seconds,omitempty"`
	MaxUses          int    `json:"max_uses,omitempty"`
}

// CreatedInvite answers POST /admin/invites. URL holds the secret code and is
// in this answer only.
type CreatedInvite struct {
	AdminInvite
	URL string `json:"url"`
}

// AdminChannelList is the body of GET /admin/channels: public and private
// channels with names and member counts, never their content.
type AdminChannelList struct {
	Items []Channel `json:"items"`
}

// UpdateAdminChannelRequest is the body of PATCH /admin/channels/{id}.
// RetentionDays is the JSON number, null (keep forever) or absent.
type UpdateAdminChannelRequest struct {
	RetentionDays json.RawMessage `json:"retention_days,omitempty"`
	IsDefault     *bool           `json:"is_default,omitempty"`
	Archived      *bool           `json:"archived,omitempty"`
}

// AuditEntry is one audit log row.
type AuditEntry struct {
	ID         string          `json:"id"`
	At         string          `json:"at"`
	ActorID    *string         `json:"actor_id"`
	ActorKind  string          `json:"actor_kind"`
	ActorIP    string          `json:"actor_ip"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id"`
	Detail     json.RawMessage `json:"detail"`
}

// AuditPage is the body of GET /admin/audit; NextCursor is null at the end.
type AuditPage struct {
	Items      []AuditEntry `json:"items"`
	NextCursor *string      `json:"next_cursor"`
}

// AdminTLS is the body of GET /admin/tls.
type AdminTLS struct {
	Mode            string   `json:"mode"`
	CAFingerprint   string   `json:"ca_fingerprint"`
	LeafFingerprint string   `json:"leaf_fingerprint"`
	LeafNotAfter    string   `json:"leaf_not_after"`
	SANs            []string `json:"sans"`
}
