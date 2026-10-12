package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/internal/chat"
)

// Security headers of every file response. The sandbox CSP keeps scripts in
// an uploaded SVG or HTML file from running if a browser ever renders it.
const (
	fileCSP       = "sandbox; default-src 'none'; img-src 'self'; media-src 'self'"
	fileCacheCtrl = "private, max-age=31536000, immutable"
)

// OpenOptions select what Open returns.
type OpenOptions struct {
	// Thumb serves the thumbnail instead of the file.
	Thumb bool
	// Download forces Content-Disposition: attachment.
	Download bool
}

// Content is an opened file ready to be served. The caller closes it.
type Content struct {
	io.ReadSeekCloser
	// ModTime is the upload time; the content never changes after it.
	ModTime time.Time
	// ContentType is the file's own type for allowlisted inline types and
	// application/octet-stream for everything else.
	ContentType string
	// Disposition is the Content-Disposition header value.
	Disposition string
	// ETag is the quoted validator derived from the SHA-256.
	ETag string
}

// Open returns the file id (or its thumbnail) for actor. A file is visible to
// its uploader, to anyone who may view a channel that holds a live message
// attaching it, and, when it is somebody's avatar, to every signed-in user.
// Everything else, including files that do not exist, is ErrNotFound.
func (s *Service) Open(ctx context.Context, actor chat.Actor, id string, opt OpenOptions) (*Content, error) {
	row, err := scanFile(s.db.Read().QueryRowContext(ctx, `SELECT `+fileCols+` FROM files WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load file: %w", err)
	}
	ok, err := s.canView(ctx, actor, row)
	if err != nil {
		return nil, err
	}
	if !ok || !validSHA(row.SHA) || (opt.Thumb && !row.HasThumb) {
		return nil, ErrNotFound
	}
	path := s.blobPath(row.SHA)
	contentType, etag := octetStream, `"`+row.SHA+`"`
	if opt.Thumb {
		path += thumbSuffix
		etag = `"` + row.SHA + `-thumb"`
		if row.ThumbMime == "image/jpeg" || row.ThumbMime == "image/png" {
			contentType = row.ThumbMime
		}
	} else if IsInline(row.Category, row.Mime) {
		contentType = row.Mime
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		s.log.Error("file blob is missing", "file_id", row.ID)
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	disposition := "attachment"
	if contentType != octetStream && !opt.Download {
		disposition = "inline"
	}
	name := row.Name
	if opt.Thumb {
		name = "thumbnail"
	}
	return &Content{
		ReadSeekCloser: f, ModTime: time.UnixMilli(row.CreatedAt), ContentType: contentType,
		Disposition: contentDisposition(disposition, name), ETag: etag,
	}, nil
}

// canView applies the visibility rule of file serving. An app reads file
// content only with messages:read, whatever else it may see (its own upload,
// an avatar, a channel it is in); channels:read alone is not enough.
func (s *Service) canView(ctx context.Context, actor chat.Actor, f fileRow) (bool, error) {
	if actor.IsApp() && !actor.HasScope(chat.ScopeMessagesRead) {
		return false, nil
	}
	if f.UploaderID == actor.ID {
		return true, nil
	}
	db := s.db.Read()
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM users WHERE avatar_file_id = ? LIMIT 1`, f.ID).Scan(&one)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("check avatar: %w", err)
	}
	rows, err := db.QueryContext(ctx, `SELECT c.id, c.kind, c.archived_at IS NOT NULL, m.user_id IS NOT NULL, COALESCE(m.role = 'owner', 0)
		FROM attachments a
		JOIN messages ms ON ms.id = a.message_id AND ms.deleted_at IS NULL
		JOIN channels c ON c.id = ms.channel_id
		LEFT JOIN memberships m ON m.channel_id = c.id AND m.user_id = ?
		WHERE a.file_id = ?`, actor.ID, f.ID)
	if err != nil {
		return false, fmt.Errorf("check file channels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var info chat.ChannelInfo
		if err := rows.Scan(&info.ID, &info.Kind, &info.Archived, &info.Member, &info.Owner); err != nil {
			return false, fmt.Errorf("check file channels: %w", err)
		}
		if chat.Can(actor, chat.ActReadContent, info) == nil {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Serve writes c to w with the headers of file serving and honours Range,
// If-Range and If-None-Match (ETag) requests. It does not close c.
func Serve(w http.ResponseWriter, r *http.Request, c *Content) {
	h := w.Header()
	h.Set("Content-Type", c.ContentType)
	h.Set("Content-Disposition", c.Disposition)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", fileCSP)
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", fileCacheCtrl)
	h.Set("ETag", c.ETag)
	http.ServeContent(w, r, "", c.ModTime, c)
}

// contentDisposition renders a Content-Disposition value (RFC 6266) for the
// file name: a quoted ASCII filename, plus the RFC 5987 filename* form when
// the name has other characters.
func contentDisposition(kind, name string) string {
	var plain strings.Builder
	ascii := true
	for _, r := range name {
		switch {
		case r < 0x20 || r >= 0x7f:
			ascii = false
			plain.WriteByte('_')
		case r == '"' || r == '\\' || r == '%':
			plain.WriteByte('_')
		default:
			plain.WriteRune(r)
		}
	}
	out := kind + `; filename="` + plain.String() + `"`
	if !ascii {
		out += "; filename*=UTF-8''" + percentEncode(name)
	}
	return out
}

// percentEncode encodes s as RFC 5987 ext-value characters.
func percentEncode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}
