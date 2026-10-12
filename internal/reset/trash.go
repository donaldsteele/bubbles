package reset

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/store"
)

// TrashPrefix starts the name of a files directory moved aside by a reset
// (trash-<ulid>). Trash directories are deleted in the background and swept
// at every server start.
const TrashPrefix = "trash-"

// TrashFiles moves <dataDir>/files aside to <dataDir>/trash-<ulid> and
// recreates an empty files directory. The rename is instant however many
// gigabytes the directory holds; SweepTrash deletes the contents later.
func TrashFiles(dataDir string) error {
	src := filepath.Join(dataDir, config.DirFiles)
	if _, err := os.Lstat(src); err == nil {
		dst := filepath.Join(dataDir, TrashPrefix+ulid.Make().String())
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("move files aside: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("move files aside: %w", err)
	}
	if err := os.MkdirAll(src, config.DirMode); err != nil {
		return fmt.Errorf("recreate files directory: %w", err)
	}
	return nil
}

// SweepTrash deletes every trash directory of dataDir and returns how many it
// removed. It stops early when ctx is done (the rest waits for the next
// sweep). Entries that vanish meanwhile are not errors.
func SweepTrash(ctx context.Context, dataDir string) (int, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return 0, fmt.Errorf("sweep trash: %w", err)
	}
	n := 0
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), TrashPrefix) {
			continue
		}
		dir := filepath.Join(dataDir, e.Name())
		children, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		for _, c := range children {
			if err := ctx.Err(); err != nil {
				return n, err
			}
			if err := os.RemoveAll(filepath.Join(dir, c.Name())); err != nil {
				errs = append(errs, err)
			}
		}
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		n++
	}
	if len(errs) > 0 {
		return n, fmt.Errorf("sweep trash: %w", errors.Join(errs...))
	}
	return n, nil
}

// clearDir deletes everything inside dir and keeps dir itself (with its
// permissions), creating it when missing.
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return os.MkdirAll(dir, config.DirMode)
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// removeSnapshots deletes the pre-migration database snapshots in backups/.
func removeSnapshots(dataDir string) error {
	dir := filepath.Join(dataDir, config.DirBackups)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), store.SnapshotPrefix) {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// removeFiles deletes the named files of dataDir; missing ones are fine.
func removeFiles(dataDir string, names ...string) error {
	var errs []error
	for _, name := range names {
		if err := os.Remove(filepath.Join(dataDir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
