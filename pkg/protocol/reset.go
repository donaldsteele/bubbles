package protocol

// Reset modes of POST /admin/reset.
const (
	// ResetContent deletes every message, file, alert and event and bumps
	// the instance epoch; accounts, channels, apps and settings stay.
	ResetContent = "content"
	// ResetFactory wipes everything and returns the server to a first
	// start (new admin account and password) under a new instance id.
	ResetFactory = "factory"
)

// ResetPreview is the body of GET /admin/reset/preview: what a reset would
// delete, shown in the confirmation dialog. Bytes is the stored size of the
// distinct uploaded files.
type ResetPreview struct {
	Users    int   `json:"users"`
	Channels int   `json:"channels"`
	Messages int   `json:"messages"`
	Files    int   `json:"files"`
	Bytes    int64 `json:"bytes"`
}

// ResetRequest is the body of POST /admin/reset. Confirm must equal the
// instance name and Password is the admin's own password. RotateCA and
// KeepSettings apply to ResetFactory only.
type ResetRequest struct {
	Mode         string `json:"mode"`
	Confirm      string `json:"confirm"`
	Password     string `json:"password"`
	RotateCA     bool   `json:"rotate_ca,omitempty"`
	KeepSettings bool   `json:"keep_settings,omitempty"`
}

// ContentCleared answers POST /admin/reset with mode "content": the new
// instance epoch. Sessions stay valid; clients resync to empty channels.
type ContentCleared struct {
	Epoch int64 `json:"epoch"`
}

// FactoryReset answers POST /admin/reset with mode "factory": the new
// new admin account, its generated password, where to sign in and the CA
// devices must trust (CARotated says whether it changed).
type FactoryReset struct {
	AdminUsername string `json:"admin_username"`
	AdminPassword string `json:"admin_password"`
	SignInURL     string `json:"sign_in_url"`
	CARotated     bool   `json:"ca_rotated"`
	CAFingerprint string `json:"ca_fingerprint"`
}

// BackupRequest is the body of POST /admin/backup. The archive is encrypted
// with Passphrase (age, scrypt).
type BackupRequest struct {
	Passphrase string `json:"passphrase"`
}
