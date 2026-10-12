package protocol

import (
	"encoding/json"
	"time"
)

// User roles.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleGuest  = "guest"
)

// User kinds.
const (
	KindHuman = "human"
	KindApp   = "app"
)

// TimeLayout is the wire format of every timestamp: RFC 3339, UTC, with
// milliseconds.
const TimeLayout = "2006-01-02T15:04:05.000Z"

// FormatTime renders unix milliseconds in TimeLayout.
func FormatTime(unixMilli int64) string {
	return time.UnixMilli(unixMilli).UTC().Format(TimeLayout)
}

// User is the public view of an account.
type User struct {
	ID           string  `json:"id"`
	Username     string  `json:"username"`
	DisplayName  string  `json:"display_name"`
	Kind         string  `json:"kind"`
	Role         string  `json:"role"`
	AvatarFileID *string `json:"avatar_file_id"`
	StatusText   string  `json:"status_text"`
	Deactivated  bool    `json:"deactivated"`
	CreatedAt    string  `json:"created_at"`
}

// Me is the signed-in user's own view: the public fields plus private ones.
type Me struct {
	User
	Prefs      json.RawMessage `json:"prefs"`
	MustChange bool            `json:"must_change_pw"`
}
