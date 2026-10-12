// Package backup creates and restores bubbles backup archives (.bbk).
//
// An archive is a tar.gz stream, age-encrypted with a passphrase by default:
//
//	db/bubbles.db      consistent VACUUM INTO snapshot
//	files/**           uploaded blobs and thumbnails
//	tls/**             local CA, leaf certificate and keys
//	vapid.json         Web Push signing key (when present)
//	settings.json      export of the settings table (informational)
//	manifest.json      last entry: format, version, sha256 of every other entry
//
// The manifest comes last so entries can be streamed while their hashes are
// computed; restore verifies every hash before anything touches the target.
package backup

import "errors"

// Archive layout constants.
const (
	FormatVersion = 1
	ManifestName  = "manifest.json"
	SettingsName  = "settings.json"
	DBEntry       = "db/bubbles.db"
	VAPIDEntry    = "vapid.json"
	dirFiles      = "files"
	dirTLS        = "tls"
)

// Limits applied while extracting an untrusted archive.
const (
	MaxEntrySize    int64 = 16 << 30
	MaxTotalSize    int64 = 1 << 40
	MaxEntries            = 1_000_000
	maxManifestSize       = 64 << 20
)

// Errors returned by Create and Restore. Test with errors.Is.
var (
	ErrNoDatabase         = errors.New("no database found in the data directory")
	ErrPassphraseRequired = errors.New("a passphrase is required")
	ErrWrongPassphrase    = errors.New("wrong passphrase or corrupted backup")
	ErrNotBackup          = errors.New("not a bubbles backup archive")
	ErrChecksum           = errors.New("backup integrity check failed")
	ErrUnsafePath         = errors.New("backup contains an unsafe entry")
	ErrNonEmpty           = errors.New("data directory is not empty (use --force to replace its contents)")
)

// Entry describes one archived file.
type Entry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest is the last entry of every archive.
type Manifest struct {
	Format         int     `json:"format"`
	BubblesVersion string  `json:"bubbles_version"`
	CreatedAt      string  `json:"created_at"`
	InstanceID     string  `json:"instance_id"`
	SchemaVersion  int     `json:"schema_version"`
	Entries        []Entry `json:"entries"`
}
