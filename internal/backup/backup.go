package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	"github.com/donaldsteele/bubbles/internal/buildinfo"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/store"
)

// CreateOptions configures Create.
type CreateOptions struct {
	// Passphrase encrypts the archive (age scrypt). Required unless NoEncrypt.
	Passphrase string
	// NoEncrypt writes a plain tar.gz. The archive then contains the CA
	// private key in the clear; callers must warn the user.
	NoEncrypt bool
	// WorkFactor overrides the scrypt work factor (log2 N); 0 uses age's default.
	WorkFactor int
	// Now and Version are injectable for tests.
	Now     func() time.Time
	Version string
}

// Create writes a backup of dataDir to w. It does not take the data-directory
// lock: the database is copied with VACUUM INTO over a separate connection, so
// a running server keeps working. The temporary snapshot is always removed.
func Create(ctx context.Context, dataDir string, w io.Writer, opts CreateOptions) error {
	if !opts.NoEncrypt && opts.Passphrase == "" {
		return ErrPassphraseRequired
	}
	dbPath := filepath.Join(dataDir, store.FileName)
	if st, serr := os.Stat(dbPath); serr != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", ErrNoDatabase, dataDir)
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	version := opts.Version
	if version == "" {
		version = buildinfo.Version
	}

	tmpRoot := filepath.Join(dataDir, config.DirTmp)
	if err := os.MkdirAll(tmpRoot, config.DirMode); err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	snapDir, err := os.MkdirTemp(tmpRoot, "backup-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(snapDir)
	snapPath := filepath.Join(snapDir, store.FileName)

	if err := vacuumInto(ctx, dbPath, snapPath); err != nil {
		return err
	}
	meta, err := readSnapshotMeta(ctx, snapPath)
	if err != nil {
		return err
	}
	settingsJSON, err := json.MarshalIndent(map[string]any{"settings": meta.settings}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}

	out := w
	var encW io.WriteCloser
	if !opts.NoEncrypt {
		rcpt, rerr := age.NewScryptRecipient(opts.Passphrase)
		if rerr != nil {
			return fmt.Errorf("passphrase: %w", rerr)
		}
		if opts.WorkFactor > 0 {
			rcpt.SetWorkFactor(opts.WorkFactor)
		}
		encW, err = age.Encrypt(w, rcpt)
		if err != nil {
			return fmt.Errorf("encrypt: %w", err)
		}
		out = encW
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	created := now().UTC()
	a := &archiver{ctx: ctx, tw: tw, mod: created}

	if err := a.addFile(DBEntry, snapPath); err != nil {
		return err
	}
	for _, d := range []string{dirFiles, dirTLS} {
		if err := a.addTree(d, filepath.Join(dataDir, d)); err != nil {
			return err
		}
	}
	if err := a.addFile(VAPIDEntry, filepath.Join(dataDir, VAPIDEntry)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := a.addBytes(SettingsName, settingsJSON); err != nil {
		return err
	}
	sort.Slice(a.entries, func(i, j int) bool { return a.entries[i].Path < a.entries[j].Path })
	man, err := json.MarshalIndent(Manifest{
		Format:         FormatVersion,
		BubblesVersion: version,
		CreatedAt:      created.Format(time.RFC3339),
		InstanceID:     meta.instanceID,
		SchemaVersion:  meta.schema,
		Entries:        a.entries,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := writeTarBytes(tw, ManifestName, man, created); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("finish archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("finish archive: %w", err)
	}
	if encW != nil {
		if err := encW.Close(); err != nil {
			return fmt.Errorf("finish encryption: %w", err)
		}
	}
	return nil
}

func sqliteDSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	return path + "?" + q.Encode()
}

// vacuumInto copies the live database to dst through its own connection.
func vacuumInto(ctx context.Context, src, dst string) error {
	db, err := sql.Open("sqlite", sqliteDSN(src))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	return nil
}

type snapshotMeta struct {
	instanceID string
	schema     int
	settings   map[string]json.RawMessage
}

func readSnapshotMeta(ctx context.Context, path string) (m snapshotMeta, err error) {
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return m, fmt.Errorf("open snapshot: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), '') FROM instance`).Scan(&m.instanceID); err != nil {
		return m, fmt.Errorf("read snapshot: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&m.schema); err != nil {
		return m, fmt.Errorf("read snapshot: %w", err)
	}
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return m, fmt.Errorf("read snapshot: %w", err)
	}
	defer rows.Close()
	m.settings = map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return m, fmt.Errorf("read snapshot: %w", err)
		}
		m.settings[k] = json.RawMessage(v)
	}
	return m, rows.Err()
}

type archiver struct {
	ctx     context.Context
	tw      *tar.Writer
	mod     time.Time
	entries []Entry
}

func writeTarBytes(tw *tar.Writer, name string, data []byte, mod time.Time) error {
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: mod, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	return nil
}

func (a *archiver) addBytes(name string, data []byte) error {
	if err := writeTarBytes(a.tw, name, data, a.mod); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	a.entries = append(a.entries, Entry{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	return nil
}

// addFile streams one regular file. A missing file returns an error wrapping
// fs.ErrNotExist before anything is written to the archive.
func (a *archiver) addFile(name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return fmt.Errorf("open %s: %w", name, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", name, err)
	}
	if !st.Mode().IsRegular() {
		return nil
	}
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime(), Typeflag: tar.TypeReg}
	if err := a.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(a.tw, h), io.LimitReader(&ctxReader{ctx: a.ctx, r: f}, st.Size()))
	if err != nil {
		return fmt.Errorf("archive %s: %w", name, err)
	}
	if n != st.Size() {
		return fmt.Errorf("archive %s: file shrank while being read", name)
	}
	a.entries = append(a.entries, Entry{Path: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
	return nil
}

// addTree archives every regular file under root as prefix/<relative path>.
// Symlinks, temp files and anything that disappears mid-walk are skipped.
func (a *archiver) addTree(prefix, root string) error {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".tmp-") {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if err := a.addFile(prefix+"/"+filepath.ToSlash(rel), p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("archive %s: %w", prefix, err)
	}
	return nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
