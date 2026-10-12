package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Upload is one file to store.
type Upload struct {
	// UserID is the uploader.
	UserID string
	// Name is the file name the client announced; it is cleaned before use.
	Name string
	// Body is the file content. It is read to its end or until the size limit.
	Body io.Reader
	// SizeHint is the announced size of the request, or 0 when unknown; it
	// only makes the free-space check stricter.
	SizeHint int64
}

// Begin reserves one of the user's concurrent upload slots. The returned
// function releases it. A user with MaxConcurrentUploads uploads in flight
// gets ErrTooManyUploads.
func (s *Service) Begin(userID string) (release func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[userID] >= MaxConcurrentUploads {
		return nil, ErrTooManyUploads
	}
	s.inflight[userID]++
	var done bool
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if done {
			return
		}
		done = true
		if s.inflight[userID]--; s.inflight[userID] <= 0 {
			delete(s.inflight, userID)
		}
	}, nil
}

// tmpFiles tracks the spool files of one upload so every exit path removes
// what it created.
type tmpFiles struct{ paths []string }

func (t *tmpFiles) add(p string) { t.paths = append(t.paths, p) }

func (t *tmpFiles) cleanup() {
	for _, p := range t.paths {
		_ = os.Remove(p)
	}
}

// stored is the outcome of processing an upload: the bytes to put in the
// store and the facts recorded in the files row.
type stored struct {
	path      string
	sha       string
	size      int64
	mime      string
	category  string
	width     *int
	height    *int
	stripped  bool
	thumb     []byte
	thumbMime string
}

// Upload stores one file for in.UserID and returns its wire view. The content
// is spooled to <data>/tmp while its SHA-256 is computed, sniffed, stripped of
// metadata and thumbnailed when it is a supported image, then moved into the
// content-addressed store. Errors: *TooLargeError (file larger than
// upload_max_bytes, or quota exceeded), ErrInsufficientStorage, ErrBadUpload.
func (s *Service) Upload(ctx context.Context, in Upload) (protocol.File, error) {
	if err := s.checkSpace(in.SizeHint); err != nil {
		return protocol.File{}, err
	}
	quota := s.settings.UserQuotaBytes()
	if quota > 0 {
		used, err := s.UsedBytes(ctx, in.UserID)
		if err != nil {
			return protocol.File{}, err
		}
		if used >= quota {
			return protocol.File{}, &TooLargeError{Quota: true, Limit: quota, Used: used}
		}
	}
	var tmps tmpFiles
	defer tmps.cleanup()

	sp, err := s.spool(ctx, in.Body, s.settings.UploadMaxBytes(), &tmps)
	if err != nil {
		return protocol.File{}, err
	}
	kind := Sniff(sp.head, in.Name)
	out := stored{path: sp.path, sha: sp.sha, size: sp.size, mime: kind.Mime, category: protocol.FileOther}
	if kind.Inline {
		out.category = kind.Category
	}
	if kind.Format != FormatNone {
		p, err := s.processImage(ctx, sp, kind, &tmps)
		if err != nil {
			return protocol.File{}, err
		}
		if p != nil {
			out = *p
			out.mime, out.category = kind.Mime, kind.Category
		} else {
			// Not processed (corrupt, or over the pixel cap): a plain
			// download that is never shown inline.
			out.category = protocol.FileOther
		}
	}
	row, err := s.commit(ctx, in, out, quota)
	if err != nil {
		return protocol.File{}, err
	}
	return row.wire(), nil
}

// commit places the blob in the store and inserts the files row, atomically
// with respect to blob removal.
func (s *Service) commit(ctx context.Context, in Upload, out stored, quota int64) (fileRow, error) {
	s.casMu.Lock()
	defer s.casMu.Unlock()
	created, err := s.place(out.path, out.sha, out.thumb)
	if err != nil {
		if isDiskFull(err) {
			return fileRow{}, ErrInsufficientStorage
		}
		return fileRow{}, err
	}
	row := fileRow{
		SHA: out.sha, Size: out.size, Mime: out.mime, Category: out.category, Name: CleanName(in.Name),
		Width: out.width, Height: out.height, HasThumb: out.thumb != nil, ThumbMime: out.thumbMime,
		MetaStripped: out.stripped, UploaderID: in.UserID,
	}
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		if quota > 0 {
			var used int64
			err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM files WHERE uploader_id = ?`, in.UserID).Scan(&used)
			if err != nil {
				return fmt.Errorf("sum file sizes: %w", err)
			}
			if used+out.size > quota {
				return &TooLargeError{Quota: true, Limit: quota, Used: used}
			}
		}
		row.ID, row.CreatedAt = tx.NewID(), tx.Now()
		var thumbMime any
		if row.HasThumb {
			thumbMime = row.ThumbMime
		}
		_, err := tx.Exec(`INSERT INTO files (`+fileCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.ID, row.SHA, row.Size, row.Mime, row.Category, row.Name, row.Width, row.Height,
			boolInt(row.HasThumb), thumbMime, boolInt(row.MetaStripped), row.UploaderID, row.CreatedAt)
		if err != nil {
			return fmt.Errorf("insert file: %w", err)
		}
		return nil
	})
	if err != nil {
		if created {
			s.removeBlob(out.sha)
		}
		return fileRow{}, err
	}
	return row, nil
}

// spooled is an upload written to the temporary directory.
type spooled struct {
	path string
	sha  string
	size int64
	head []byte
}

// errWriter remembers the first error of the underlying writer, so a failed
// write can be told from a failed read of the request body.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if err != nil && e.err == nil {
		e.err = err
	}
	return n, err
}

// headWriter keeps the first sniffLen bytes that pass through it.
type headWriter struct {
	w    io.Writer
	head []byte
}

func (h *headWriter) Write(p []byte) (int, error) {
	if room := sniffLen - len(h.head); room > 0 {
		h.head = append(h.head, p[:min(room, len(p))]...)
	}
	return h.w.Write(p)
}

// spool copies body into a new temporary file, hashing it on the way, and
// refuses more than maxBytes.
func (s *Service) spool(ctx context.Context, body io.Reader, maxBytes int64, tmps *tmpFiles) (spooled, error) {
	f, err := os.OpenFile(filepath.Join(s.tmpDir, tmpName("up-")), os.O_CREATE|os.O_EXCL|os.O_WRONLY, config.FileMode)
	if err != nil {
		return spooled{}, spoolError(err)
	}
	tmps.add(f.Name())
	h := sha256.New()
	sink := &errWriter{w: io.MultiWriter(f, h)}
	hw := &headWriter{w: sink}
	n, copyErr := io.Copy(hw, io.LimitReader(body, maxBytes+1))
	closeErr := f.Close()
	switch {
	case sink.err != nil:
		return spooled{}, spoolError(sink.err)
	case copyErr != nil:
		var tooBig *http.MaxBytesError
		if errors.As(copyErr, &tooBig) {
			return spooled{}, &TooLargeError{Limit: maxBytes}
		}
		if ctx.Err() != nil {
			return spooled{}, ctx.Err()
		}
		return spooled{}, fmt.Errorf("%w: %v", ErrBadUpload, copyErr)
	case closeErr != nil:
		return spooled{}, spoolError(closeErr)
	case n > maxBytes:
		return spooled{}, &TooLargeError{Limit: maxBytes}
	}
	return spooled{path: f.Name(), sha: hex.EncodeToString(h.Sum(nil)), size: n, head: hw.head}, nil
}

func spoolError(err error) error {
	if isDiskFull(err) {
		return ErrInsufficientStorage
	}
	return fmt.Errorf("spool upload: %w", err)
}

// writeSpool runs fn against a new temporary file and returns the file's
// location, SHA-256 and size.
func (s *Service) writeSpool(tmps *tmpFiles, fn func(w io.Writer) error) (spooled, error) {
	f, err := os.OpenFile(filepath.Join(s.tmpDir, tmpName("pr-")), os.O_CREATE|os.O_EXCL|os.O_WRONLY, config.FileMode)
	if err != nil {
		return spooled{}, spoolError(err)
	}
	tmps.add(f.Name())
	h := sha256.New()
	sink := &errWriter{w: io.MultiWriter(f, h)}
	cw := &countWriter{w: sink}
	fnErr := fn(cw)
	closeErr := f.Close()
	switch {
	case sink.err != nil:
		return spooled{}, spoolError(sink.err)
	case fnErr != nil:
		return spooled{}, fnErr
	case closeErr != nil:
		return spooled{}, spoolError(closeErr)
	}
	return spooled{path: f.Name(), sha: hex.EncodeToString(h.Sum(nil)), size: cw.n}, nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// processImage strips the metadata of a supported image, applies its EXIF
// orientation and makes its thumbnail. It returns nil (and no error) when the
// file cannot be processed, which makes it a plain download: unreadable
// headers, a corrupt stream or more than MaxPixels pixels. Every full decode
// holds its pixels in decodeBudget until its output is encoded, and waits for
// them while the budget is full; only the header is read outside the budget.
func (s *Service) processImage(ctx context.Context, sp spooled, kind Kind, tmps *tmpFiles) (*stored, error) {
	f, err := os.Open(sp.path)
	if err != nil {
		return nil, fmt.Errorf("open spooled upload: %w", err)
	}
	defer f.Close()
	cfg, _, derr := image.DecodeConfig(f)
	w, h := cfg.Width, cfg.Height
	if derr != nil {
		// Animated WebP is not decodable by the standard decoder; its
		// canvas size still tells how big it is.
		if kind.Format != FormatWebP {
			return nil, nil
		}
		cw, ch, ok := webpCanvas(f, sp.size)
		if !ok {
			return nil, nil
		}
		w, h = cw, ch
	}
	if w <= 0 || h <= 0 || int64(w)*int64(h) > MaxPixels {
		return nil, nil
	}
	pixels := int64(w) * int64(h)
	out := &stored{path: sp.path, sha: sp.sha, size: sp.size, stripped: true}
	var decoded image.Image
	rewind := func() error { _, err := f.Seek(0, io.SeekStart); return err }
	// run writes fn's output to a new spool file and makes it the stored
	// content. It reports false for a file that is malformed (store it
	// plain) and returns real I/O errors.
	run := func(fn func(w io.Writer) error) (bool, error) {
		if err := rewind(); err != nil {
			return false, err
		}
		res, err := s.writeSpool(tmps, fn)
		if errors.Is(err, ErrMalformed) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		out.path, out.sha, out.size = res.path, res.sha, res.size
		return true, nil
	}
	switch kind.Format {
	case FormatJPEG:
		if err := rewind(); err != nil {
			return nil, err
		}
		orientation, icc := jpegMeta(f)
		if orientation > 1 {
			if err := rewind(); err != nil {
				return nil, err
			}
			// Held until the rotated copy is encoded and thumbnailed.
			release, err := decodeBudget.acquire(ctx, pixels)
			if err != nil {
				return nil, err
			}
			defer release()
			done := decodeHook(pixels)
			defer done()
			img, err := jpeg.Decode(f)
			if err != nil {
				return nil, nil
			}
			rot := orient(img, orientation)
			if ok, err := run(func(w io.Writer) error { return encodeJPEG(w, rot, icc) }); !ok {
				return nil, err
			}
			w, h = rot.Bounds().Dx(), rot.Bounds().Dy()
			decoded = rot
		} else if ok, err := run(func(w io.Writer) error { return StripJPEG(w, f) }); !ok {
			return nil, err
		}
	case FormatPNG:
		if ok, err := run(func(w io.Writer) error { return StripPNG(w, f) }); !ok {
			return nil, err
		}
	case FormatWebP:
		if ok, err := run(func(w io.Writer) error { return StripWebP(w, f, sp.size) }); !ok {
			return nil, err
		}
	}
	out.width, out.height = &w, &h
	if _, _, ok := thumbSize(w, h); ok {
		if decoded == nil {
			release, err := decodeBudget.acquire(ctx, pixels)
			if err != nil {
				return nil, err
			}
			defer release()
			done := decodeHook(pixels)
			defer done()
			decoded = decodeFile(out.path)
		}
		if decoded != nil {
			if data, mime, err := makeThumb(decoded); err == nil {
				out.thumb, out.thumbMime = data, mime
			}
		}
	}
	return out, nil
}

// decodeFile decodes the image at path, or returns nil when it cannot.
func decodeFile(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil
	}
	return img
}
