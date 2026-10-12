// Package files is the file domain service: streaming uploads into a
// content-addressed store, metadata stripping, thumbnails, quotas, the
// permission check and headers of file serving, and the binding of uploaded
// files to messages.
//
// Blobs live at <data>/files/<sha[0:2]>/<sha[2:4]>/<sha> (the thumbnail next
// to it as <sha>.thumb), keyed by the SHA-256 of the bytes that are stored,
// so identical uploads share one blob. Uploads are spooled in <data>/tmp.
// File names are user data: nothing in this package logs them.
package files

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// MaxConcurrentUploads is how many uploads one user may have in flight.
const MaxConcurrentUploads = 5

// MaxPerMessage is the most files one message carries.
const MaxPerMessage = 10

// Options configure a Service.
type Options struct {
	// DB is the store; required.
	DB *store.DB
	// Settings supplies upload_max_bytes, user_quota_bytes and
	// disk_reserve_bytes; required.
	Settings *settings.Store
	// DataDir is the data directory; blobs go to <DataDir>/files and the
	// upload spool to <DataDir>/tmp. Required.
	DataDir string
	// Logger receives non-fatal errors; nil discards them.
	Logger *slog.Logger
	// Now is the clock of the tmp sweep; nil means time.Now.
	Now func() time.Time
	// FreeSpace reports the bytes available on the volume holding dir; nil
	// means the operating system's answer. Tests inject it.
	FreeSpace func(dir string) (uint64, error)
}

// Service is the file domain service.
type Service struct {
	db        *store.DB
	settings  *settings.Store
	dataDir   string
	filesDir  string
	tmpDir    string
	log       *slog.Logger
	now       func() time.Time
	freeSpace func(dir string) (uint64, error)

	// casMu serializes placing a blob in the store (upload) with removing
	// unreferenced blobs, so a blob is never removed between an upload's
	// existence check and its row becoming visible.
	casMu sync.Mutex

	mu       sync.Mutex
	inflight map[string]int
}

// New returns a Service and makes sure its directories exist.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Settings == nil || o.DataDir == "" {
		return nil, errors.New("files: DB, Settings and DataDir are required")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.FreeSpace == nil {
		o.FreeSpace = osFreeSpace
	}
	s := &Service{
		db: o.DB, settings: o.Settings, dataDir: o.DataDir,
		filesDir: filepath.Join(o.DataDir, config.DirFiles), tmpDir: filepath.Join(o.DataDir, config.DirTmp),
		log: o.Logger, now: o.Now, freeSpace: o.FreeSpace, inflight: map[string]int{},
	}
	for _, d := range []string{s.filesDir, s.tmpDir} {
		if err := os.MkdirAll(d, config.DirMode); err != nil {
			return nil, fmt.Errorf("files: create %s: %w", filepath.Base(d), err)
		}
	}
	return s, nil
}

// MaxUploadBytes is the current per-file size limit (upload_max_bytes).
func (s *Service) MaxUploadBytes() int64 { return s.settings.UploadMaxBytes() }

func ulidString() string { return ulid.Make().String() }

// fileRow is a files row.
type fileRow struct {
	ID           string
	SHA          string
	Size         int64
	Mime         string
	Category     string
	Name         string
	Width        *int
	Height       *int
	HasThumb     bool
	ThumbMime    string
	MetaStripped bool
	UploaderID   string
	CreatedAt    int64
}

const fileCols = `id, sha256, size, mime, category, original_name, width, height, has_thumb, thumb_mime, meta_stripped, uploader_id, created_at`

type scanner interface{ Scan(dest ...any) error }

func scanFile(sc scanner) (fileRow, error) {
	var (
		f             fileRow
		width, height sql.NullInt64
		thumbMime     sql.NullString
		hasThumb      int
		stripped      int
	)
	err := sc.Scan(&f.ID, &f.SHA, &f.Size, &f.Mime, &f.Category, &f.Name, &width, &height, &hasThumb, &thumbMime, &stripped, &f.UploaderID, &f.CreatedAt)
	if err != nil {
		return f, err
	}
	if width.Valid {
		v := int(width.Int64)
		f.Width = &v
	}
	if height.Valid {
		v := int(height.Int64)
		f.Height = &v
	}
	f.HasThumb, f.MetaStripped = hasThumb == 1, stripped == 1
	f.ThumbMime = thumbMime.String
	return f, nil
}

// wire is the protocol view of the row.
func (f fileRow) wire() protocol.File {
	out := protocol.File{
		ID: f.ID, Name: f.Name, Size: f.Size, Mime: f.Mime, Category: f.Category,
		Width: f.Width, Height: f.Height, HasThumb: f.HasThumb, MetaStripped: f.MetaStripped,
		URL: protocol.FileURL(f.ID, f.Name),
	}
	if f.HasThumb {
		t := protocol.FileThumbURL(f.ID, f.Name)
		out.ThumbURL = &t
	}
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
