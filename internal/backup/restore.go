package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/store"
)

// RestoreOptions configures Restore.
type RestoreOptions struct {
	// Passphrase decrypts an encrypted archive; plain archives ignore it.
	Passphrase string
	// Force replaces an existing, non-empty data directory's contents.
	Force bool
}

const (
	ageMagic     = "age-encryption.org/"
	maxWorkScale = 22
)

// managed lists the top-level data-directory entries a restore owns.
var managed = []string{store.FileName, store.FileName + "-wal", store.FileName + "-shm", dirFiles, dirTLS, VAPIDEntry, auth.AdminPasswordFile}

// Restore unpacks the archive from r into dataDir. It refuses while a server
// holds the data-directory lock (error wrapping config.ErrLocked) and when the
// directory already has content unless opts.Force is set. The archive is
// extracted into a staging directory beside dataDir, every entry is verified
// against the manifest hashes, and only then are the pieces renamed into place.
// On any failure dataDir is left as it was.
func Restore(ctx context.Context, r io.Reader, dataDir string, opts RestoreOptions) (err error) {
	plain, err := openArchive(r, opts.Passphrase)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotBackup, err)
	}
	defer gz.Close()

	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(dataDir)
	existed := statErr == nil
	if err := os.MkdirAll(dataDir, config.DirMode); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	lock, err := config.AcquireLock(dataDir)
	if err != nil {
		if !existed {
			_ = os.Remove(dataDir)
		}
		return err
	}
	defer func() {
		_ = lock.Release()
		if err != nil && !existed {
			_ = os.RemoveAll(dataDir)
		}
	}()
	if !opts.Force {
		has, herr := hasContent(dataDir)
		if herr != nil {
			return herr
		}
		if has {
			return ErrNonEmpty
		}
	}

	staging, err := os.MkdirTemp(filepath.Dir(dataDir), ".bubbles-restore-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := config.EnsureDataDir(staging); err != nil {
		return fmt.Errorf("prepare staging directory: %w", err)
	}
	if err := extract(ctx, gz, staging); err != nil {
		return err
	}
	if err := swapIn(staging, dataDir); err != nil {
		return err
	}
	return config.EnsureDataDir(dataDir)
}

// openArchive detects an age header or plain gzip and returns the plaintext stream.
func openArchive(r io.Reader, passphrase string) (io.Reader, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	head, _ := br.Peek(len(ageMagic))
	switch {
	case bytes.Equal(head, []byte(ageMagic)):
		if passphrase == "" {
			return nil, ErrPassphraseRequired
		}
		id, err := age.NewScryptIdentity(passphrase)
		if err != nil {
			return nil, fmt.Errorf("passphrase: %w", err)
		}
		id.SetMaxWorkFactor(maxWorkScale)
		pr, err := age.Decrypt(br, id)
		if err != nil {
			var nm *age.NoIdentityMatchError
			if errors.As(err, &nm) {
				return nil, ErrWrongPassphrase
			}
			return nil, fmt.Errorf("%w: %v", ErrNotBackup, err)
		}
		return pr, nil
	case len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return br, nil
	default:
		return nil, ErrNotBackup
	}
}

func hasContent(dir string) (bool, error) {
	found := false
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if p == filepath.Join(dir, config.LockFileName) {
			return nil
		}
		found = true
		return fs.SkipAll
	})
	return found, err
}

type fileSum struct {
	size int64
	sum  string
}

// destination maps an archive path to its staging-relative path. ok is false
// for informational entries (settings.json) that are verified but not restored.
func destination(name string) (rel string, ok bool, err error) {
	switch {
	case name == DBEntry:
		return store.FileName, true, nil
	case name == VAPIDEntry:
		return VAPIDEntry, true, nil
	case name == SettingsName:
		return "", false, nil
	case strings.HasPrefix(name, dirFiles+"/"), strings.HasPrefix(name, dirTLS+"/"):
		return filepath.FromSlash(name), true, nil
	}
	return "", false, fmt.Errorf("%w: unexpected entry", ErrUnsafePath)
}

// safeName rejects absolute, parent-relative and otherwise suspicious names.
func safeName(name string) error {
	if name == "" || len(name) > 1024 || path.IsAbs(name) || path.Clean(name) != name {
		return fmt.Errorf("%w: bad entry name", ErrUnsafePath)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return fmt.Errorf("%w: bad entry name", ErrUnsafePath)
		}
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f || strings.ContainsRune(`\:*?"<>|`, c) {
			return fmt.Errorf("%w: bad entry name", ErrUnsafePath)
		}
	}
	return nil
}

func extract(ctx context.Context, r io.Reader, staging string) error {
	tr := tar.NewReader(r)
	got := map[string]fileSum{}
	var manifest *Manifest
	var total int64
	for n := 0; ; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if n >= MaxEntries {
			return fmt.Errorf("%w: too many entries", ErrUnsafePath)
		}
		if err := safeName(hdr.Name); err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("%w: entry is not a regular file", ErrUnsafePath)
		}
		if hdr.Size < 0 || hdr.Size > MaxEntrySize {
			return fmt.Errorf("%w: entry too large", ErrUnsafePath)
		}
		if total += hdr.Size; total > MaxTotalSize {
			return fmt.Errorf("%w: archive too large", ErrUnsafePath)
		}
		if manifest != nil {
			return fmt.Errorf("%w: data after manifest", ErrUnsafePath)
		}
		if hdr.Name == ManifestName {
			if hdr.Size > maxManifestSize {
				return fmt.Errorf("%w: manifest too large", ErrUnsafePath)
			}
			raw, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
			if err != nil {
				return fmt.Errorf("read archive: %w", err)
			}
			var m Manifest
			if err := json.Unmarshal(raw, &m); err != nil {
				return fmt.Errorf("%w: manifest: %v", ErrNotBackup, err)
			}
			manifest = &m
			continue
		}
		if _, dup := got[hdr.Name]; dup {
			return fmt.Errorf("%w: duplicate entry", ErrUnsafePath)
		}
		rel, write, err := destination(hdr.Name)
		if err != nil {
			return err
		}
		h := sha256.New()
		var dst io.Writer = h
		var f *os.File
		if write {
			target := filepath.Join(staging, rel)
			if err := os.MkdirAll(filepath.Dir(target), config.DirMode); err != nil {
				return fmt.Errorf("extract: %w", err)
			}
			f, err = os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, config.FileMode)
			if err != nil {
				return fmt.Errorf("extract: %w", err)
			}
			dst = io.MultiWriter(f, h)
		}
		written, cerr := io.Copy(dst, tr)
		if f != nil {
			if cerr == nil {
				cerr = f.Sync()
			}
			if clerr := f.Close(); cerr == nil {
				cerr = clerr
			}
		}
		if cerr != nil {
			return fmt.Errorf("extract: %w", cerr)
		}
		got[hdr.Name] = fileSum{size: written, sum: hex.EncodeToString(h.Sum(nil))}
	}
	if manifest == nil {
		return fmt.Errorf("%w: manifest missing", ErrNotBackup)
	}
	if manifest.Format != FormatVersion {
		return fmt.Errorf("%w: unsupported format %d", ErrNotBackup, manifest.Format)
	}
	if len(manifest.Entries) != len(got) {
		return fmt.Errorf("%w: entry list does not match manifest", ErrChecksum)
	}
	for _, e := range manifest.Entries {
		g, ok := got[e.Path]
		if !ok {
			return fmt.Errorf("%w: %s missing from archive", ErrChecksum, e.Path)
		}
		if g.size != e.Size || !strings.EqualFold(g.sum, e.SHA256) {
			return fmt.Errorf("%w: sha256 mismatch for %s", ErrChecksum, e.Path)
		}
	}
	if _, ok := got[DBEntry]; !ok {
		return fmt.Errorf("%w: database missing", ErrNotBackup)
	}
	return nil
}

// renameFile performs the moves used by swapIn. It is a variable so tests can
// inject failures on a chosen call.
var renameFile = os.Rename

// swapIn replaces the restore-owned entries of dataDir with those in staging
// transactionally. Existing managed entries are first moved aside into a
// `.bubbles-restore-old-*` directory, then staged entries are moved in. If any
// move fails, installed entries are moved back out to staging and the aside
// entries are restored, leaving dataDir as it was. The aside directory is
// removed only after every step succeeded.
func swapIn(staging, dataDir string) (err error) {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	aside, err := os.MkdirTemp(dataDir, ".bubbles-restore-old-")
	if err != nil {
		return fmt.Errorf("create rollback directory: %w", err)
	}
	var movedAside, installed []string
	defer func() {
		if err == nil {
			_ = os.RemoveAll(aside)
			return
		}
		err = rollbackSwap(err, staging, dataDir, aside, installed, movedAside)
	}()

	for _, name := range managed {
		src := filepath.Join(dataDir, name)
		if _, lerr := os.Lstat(src); lerr != nil {
			continue
		}
		if rerr := renameFile(src, filepath.Join(aside, name)); rerr != nil {
			return fmt.Errorf("clear %s: %w", name, rerr)
		}
		movedAside = append(movedAside, name)
	}
	for _, e := range entries {
		name := e.Name()
		dst := filepath.Join(dataDir, name)
		if !isManaged(name) {
			if _, lerr := os.Lstat(dst); lerr == nil {
				continue
			}
		}
		if rerr := renameFile(filepath.Join(staging, name), dst); rerr != nil {
			return fmt.Errorf("install %s: %w", name, rerr)
		}
		installed = append(installed, name)
	}
	return nil
}

// rollbackSwap undoes a partially applied swapIn. Installed entries are moved
// back into staging and aside entries are restored into dataDir. Rollback
// failures are joined onto cause; the aside directory is removed only when it
// is empty again.
func rollbackSwap(cause error, staging, dataDir, aside string, installed, movedAside []string) error {
	var rbErr []error
	for i := len(installed) - 1; i >= 0; i-- {
		name := installed[i]
		if err := renameFile(filepath.Join(dataDir, name), filepath.Join(staging, name)); err != nil {
			rbErr = append(rbErr, fmt.Errorf("rollback %s: %w", name, err))
		}
	}
	for i := len(movedAside) - 1; i >= 0; i-- {
		name := movedAside[i]
		if err := renameFile(filepath.Join(aside, name), filepath.Join(dataDir, name)); err != nil {
			rbErr = append(rbErr, fmt.Errorf("restore %s: %w", name, err))
		}
	}
	if len(rbErr) == 0 {
		_ = os.RemoveAll(aside)
		return cause
	}
	return errors.Join(append([]error{cause}, rbErr...)...)
}

func isManaged(name string) bool {
	for _, m := range managed {
		if m == name {
			return true
		}
	}
	return false
}
