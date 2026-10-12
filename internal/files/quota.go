package files

import (
	"context"
	"errors"
	"fmt"
)

// Errors of the upload path. The HTTP layer maps them to 413, 415, 429 and
// 507.
var (
	// ErrInsufficientStorage means the data volume has less free space than
	// the disk_reserve_bytes setting.
	ErrInsufficientStorage = errors.New("insufficient storage")
	// ErrTooManyUploads means the user already has the maximum number of
	// uploads in flight.
	ErrTooManyUploads = errors.New("too many concurrent uploads")
	// ErrNotFound means no such file, or one the caller may not see.
	ErrNotFound = errors.New("file not found")
	// ErrBadUpload means the request body could not be read as an upload.
	ErrBadUpload = errors.New("malformed upload")
)

// TooLargeError reports an upload that exceeds the per-file limit or the
// user's quota. Limit is the exceeded limit in bytes; for a quota Used is
// what the user stores already.
type TooLargeError struct {
	Quota bool
	Limit int64
	Used  int64
}

func (e *TooLargeError) Error() string {
	if e.Quota {
		return fmt.Sprintf("storage quota of %d bytes exceeded", e.Limit)
	}
	return fmt.Sprintf("file is larger than %d bytes", e.Limit)
}

// Details are the machine-readable details of the 413 answer.
func (e *TooLargeError) Details() map[string]any {
	if e.Quota {
		return map[string]any{"limit": "user_quota_bytes", "max_bytes": e.Limit, "used_bytes": e.Used}
	}
	return map[string]any{"limit": "upload_max_bytes", "max_bytes": e.Limit}
}

// UsedBytes returns how many bytes the files of userID occupy (every files
// row counts, whether or not it is attached yet).
func (s *Service) UsedBytes(ctx context.Context, userID string) (int64, error) {
	var used int64
	err := s.db.Read().QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM files WHERE uploader_id = ?`, userID).Scan(&used)
	if err != nil {
		return 0, fmt.Errorf("sum file sizes: %w", err)
	}
	return used, nil
}

// checkSpace refuses the upload when the data volume would be left with
// less than disk_reserve_bytes free. hint is the announced upload size, or
// 0 when unknown.
func (s *Service) checkSpace(hint int64) error {
	free, err := s.freeSpace(s.dataDir)
	if err != nil {
		return fmt.Errorf("read free disk space: %w", err)
	}
	need := uint64(max(s.settings.DiskReserveBytes(), 0))
	if hint > 0 {
		need += uint64(hint)
	}
	if free < need {
		return ErrInsufficientStorage
	}
	return nil
}

// DiskFree returns the bytes available on the data volume.
func (s *Service) DiskFree() (uint64, error) { return s.freeSpace(s.dataDir) }

// TotalBytes returns how many bytes every stored file occupies.
func (s *Service) TotalBytes(ctx context.Context) (int64, error) {
	var total int64
	err := s.db.Read().QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM files`).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum file sizes: %w", err)
	}
	return total, nil
}
