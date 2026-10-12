package files

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/internal/config"
)

// thumbSuffix is appended to a blob's path to name its thumbnail.
const thumbSuffix = ".thumb"

// validSHA reports whether s is a lower-case hex SHA-256, the only form that
// may be turned into a path.
func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// blobPath is where the content with this SHA-256 lives:
// <data>/files/<sha[0:2]>/<sha[2:4]>/<sha>.
func (s *Service) blobPath(sha string) string {
	return filepath.Join(s.filesDir, sha[0:2], sha[2:4], sha)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// place moves the spooled file at src into the content-addressed store under
// sha and writes the thumbnail next to it. When the blob exists already
// (identical content uploaded before) src is left for the caller to delete and
// nothing is written. created reports whether this call added the blob. The
// caller holds s.casMu.
func (s *Service) place(src, sha string, thumb []byte) (created bool, err error) {
	dest := s.blobPath(sha)
	if err := os.MkdirAll(filepath.Dir(dest), config.DirMode); err != nil {
		return false, fmt.Errorf("create blob directory: %w", err)
	}
	if !exists(dest) {
		if err := os.Rename(src, dest); err != nil {
			return false, fmt.Errorf("store blob: %w", err)
		}
		created = true
	}
	if thumb != nil && !exists(dest+thumbSuffix) {
		if err := s.writeThumb(dest+thumbSuffix, thumb); err != nil {
			if created {
				_ = os.Remove(dest)
			}
			return false, err
		}
	}
	return created, nil
}

// writeThumb stores the thumbnail atomically: a reader never sees half of it.
func (s *Service) writeThumb(dest string, data []byte) error {
	f, err := os.CreateTemp(s.tmpDir, "thumb-*")
	if err != nil {
		return fmt.Errorf("write thumbnail: %w", err)
	}
	name := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write thumbnail: %w", err)
	}
	if err := os.Rename(name, dest); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("store thumbnail: %w", err)
	}
	return nil
}

// removeBlob deletes a blob and its thumbnail and prunes the two directory
// levels above it when they are empty.
func (s *Service) removeBlob(sha string) {
	p := s.blobPath(sha)
	_ = os.Remove(p)
	_ = os.Remove(p + thumbSuffix)
	_ = os.Remove(filepath.Dir(p))
	_ = os.Remove(filepath.Dir(filepath.Dir(p)))
}

// RemoveUnreferenced deletes the blobs (and thumbnails) of the given SHA-256
// values that no files row refers to any more, and returns how many blobs it
// removed. Callers delete the rows first and call this after the transaction
// committed; the check is repeated here, under the same lock uploads hold,
// so an upload of identical content racing with the removal keeps its blob.
// The lock is taken per SHA-256, so uploads are not stalled for the whole list.
func (s *Service) RemoveUnreferenced(ctx context.Context, shas []string) (int, error) {
	removed := 0
	for _, sha := range shas {
		if !validSHA(sha) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		ok, err := s.removeIfOrphan(ctx, sha, time.Time{})
		if err != nil {
			return removed, err
		}
		if ok {
			removed++
		}
	}
	return removed, nil
}

// SweepOrphanBlobs walks the blob store and removes the blobs and thumbnails
// that no files row refers to, and returns how many blobs it removed. Entries
// modified within the last minAge are spared: an upload writes its blob
// before its row, so a young blob may belong to an upload still in flight.
// Each candidate is checked again under the upload lock before it goes.
func (s *Service) SweepOrphanBlobs(ctx context.Context, minAge time.Duration) (int, error) {
	recent := s.now().Add(-minAge)
	removed := 0
	err := filepath.WalkDir(s.filesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		sha, ok := blobKey(d.Name())
		if !ok || (p != s.blobPath(sha) && p != s.blobPath(sha)+thumbSuffix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.ModTime().After(recent) {
			return nil
		}
		ok, err = s.removeIfOrphan(ctx, sha, recent)
		if err != nil {
			return err
		}
		if ok {
			removed++
		}
		return nil
	})
	if err != nil {
		return removed, fmt.Errorf("sweep blobs: %w", err)
	}
	return removed, nil
}

// blobKey returns the SHA-256 a file of the blob store belongs to: the name of
// a blob, or the name of a thumbnail without its suffix.
func blobKey(name string) (string, bool) {
	if validSHA(name) {
		return name, true
	}
	if sha, ok := strings.CutSuffix(name, thumbSuffix); ok && validSHA(sha) {
		return sha, true
	}
	return "", false
}

// removeIfOrphan deletes the blob and thumbnail of sha when no files row
// refers to them, and reports whether it did. When recent is not zero, a blob
// or thumbnail modified after recent is spared. The check and the removal run
// under casMu, so an upload of the same content either finds the blob gone
// and places it again, or finds the blob and its row is already visible here.
func (s *Service) removeIfOrphan(ctx context.Context, sha string, recent time.Time) (bool, error) {
	s.casMu.Lock()
	defer s.casMu.Unlock()
	var n int
	err := s.db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE sha256 = ?`, sha).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check blob references: %w", err)
	}
	if n > 0 {
		return false, nil
	}
	p := s.blobPath(sha)
	if !exists(p) && !exists(p+thumbSuffix) {
		return false, nil
	}
	if !recent.IsZero() {
		for _, f := range []string{p, p + thumbSuffix} {
			if fi, err := os.Stat(f); err == nil && fi.ModTime().After(recent) {
				return false, nil
			}
		}
	}
	s.removeBlob(sha)
	return true, nil
}

// SweepTmp deletes the entries of the temporary directory that are older
// than maxAge (0 deletes them all) and returns how many it removed. Uploads
// that were cut off by a restart leave such files behind.
func (s *Service) SweepTmp(maxAge time.Duration) (int, error) {
	entries, err := os.ReadDir(s.tmpDir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("sweep tmp: %w", err)
	}
	cutoff := s.now().Add(-maxAge)
	removed := 0
	var errs []error
	for _, e := range entries {
		if maxAge > 0 {
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
		}
		if err := os.RemoveAll(filepath.Join(s.tmpDir, e.Name())); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// tmpName is a fresh name for a file in the temporary directory.
func tmpName(prefix string) string {
	return prefix + strings.ToLower(ulidString())
}
