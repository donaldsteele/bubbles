package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/store"
)

const testPass = "correct horse battery staple"

// seed builds a data directory with a live open database, files and TLS material.
func seed(t *testing.T) (dir string, db *store.DB) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "data")
	if err := config.EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), filepath.Join(dir, store.FileName), store.Options{})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	err = db.Do(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO settings(key, value) VALUES ('instance_name', '"Home"')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "files", "ab", "blob1"), "blob-one")
	write(t, filepath.Join(dir, "files", "thumbs", "t1.webp"), "thumb")
	write(t, filepath.Join(dir, "tls", "ca.crt"), "CERT")
	write(t, filepath.Join(dir, "tls", "ca.key"), "KEY")
	write(t, filepath.Join(dir, "vapid.json"), `{"k":1}`)
	write(t, filepath.Join(dir, "tmp", "scratch"), "not archived")
	return dir, db
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func create(t *testing.T, dir string, opts CreateOptions) []byte {
	t.Helper()
	opts.WorkFactor = 10
	var buf bytes.Buffer
	if err := Create(context.Background(), dir, &buf, opts); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return buf.Bytes()
}

func newTarget(t *testing.T) string { return filepath.Join(t.TempDir(), "restored") }

func TestRoundTripEncrypted(t *testing.T) {
	dir, db := seed(t)
	archive := create(t, dir, CreateOptions{Passphrase: testPass, Version: "v9.9.9"})
	if bytes.HasPrefix(archive, []byte{0x1f, 0x8b}) || !bytes.HasPrefix(archive, []byte("age-encryption.org/")) {
		t.Fatal("archive is not age-encrypted")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "tmp", "backup-*")); len(left) != 0 {
		t.Fatalf("temp snapshot left behind: %v", left)
	}
	// The source database stays usable (online backup, no lock).
	if err := db.Do(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE settings SET value = '"Later"' WHERE key = 'instance_name'`)
		return err
	}); err != nil {
		t.Fatalf("write after backup: %v", err)
	}

	target := newTarget(t)
	if err := Restore(context.Background(), bytes.NewReader(archive), target, RestoreOptions{Passphrase: testPass}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for rel, want := range map[string]string{
		"files/ab/blob1":       "blob-one",
		"files/thumbs/t1.webp": "thumb",
		"tls/ca.crt":           "CERT",
		"tls/ca.key":           "KEY",
		"vapid.json":           `{"k":1}`,
	} {
		if got := read(t, filepath.Join(target, filepath.FromSlash(rel))); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "tmp", "scratch")); err == nil {
		t.Error("tmp/ must not be archived")
	}
	restored, err := store.Open(context.Background(), filepath.Join(target, store.FileName), store.Options{})
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	defer restored.Close()
	var v string
	if err := restored.Read().QueryRow(`SELECT value FROM settings WHERE key = 'instance_name'`).Scan(&v); err != nil || v != `"Home"` {
		t.Fatalf("restored setting = %q, %v; want the value at backup time", v, err)
	}
	var id, srcID string
	_ = restored.Read().QueryRow(`SELECT id FROM instance`).Scan(&id)
	_ = db.Read().QueryRow(`SELECT id FROM instance`).Scan(&srcID)
	if id == "" || id != srcID {
		t.Errorf("instance id %q != source %q", id, srcID)
	}
}

func TestManifestContents(t *testing.T) {
	dir, _ := seed(t)
	archive := create(t, dir, CreateOptions{NoEncrypt: true, Version: "v1.2.3"})
	entries := readTar(t, archive)
	var m Manifest
	if err := jsonUnmarshal(entries[len(entries)-1].body, &m); err != nil {
		t.Fatal(err)
	}
	if entries[len(entries)-1].name != ManifestName || m.Format != 1 || m.BubblesVersion != "v1.2.3" || m.InstanceID == "" || m.SchemaVersion < 1 {
		t.Fatalf("bad manifest: %+v", m)
	}
	have := map[string]bool{}
	for _, e := range m.Entries {
		have[e.Path] = true
	}
	for _, p := range []string{DBEntry, "files/ab/blob1", "tls/ca.key", VAPIDEntry, SettingsName} {
		if !have[p] {
			t.Errorf("manifest lacks %s", p)
		}
	}
}

func TestPlainRoundTrip(t *testing.T) {
	dir, _ := seed(t)
	archive := create(t, dir, CreateOptions{NoEncrypt: true})
	target := newTarget(t)
	if err := Restore(context.Background(), bytes.NewReader(archive), target, RestoreOptions{}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := read(t, filepath.Join(target, "tls", "ca.key")); got != "KEY" {
		t.Errorf("ca.key = %q", got)
	}
}

func TestCreateRequiresPassphrase(t *testing.T) {
	dir, _ := seed(t)
	err := Create(context.Background(), dir, io.Discard, CreateOptions{})
	if !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("Create = %v, want ErrPassphraseRequired", err)
	}
}

func TestCreateNoDatabase(t *testing.T) {
	err := Create(context.Background(), t.TempDir(), io.Discard, CreateOptions{NoEncrypt: true})
	if !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("Create = %v, want ErrNoDatabase", err)
	}
}

func TestWrongPassphraseWritesNothing(t *testing.T) {
	dir, _ := seed(t)
	archive := create(t, dir, CreateOptions{Passphrase: testPass})
	target := newTarget(t)
	err := Restore(context.Background(), bytes.NewReader(archive), target, RestoreOptions{Passphrase: "nope nope nope"})
	if !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("Restore = %v, want ErrWrongPassphrase", err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("target was created")
	}
	err = Restore(context.Background(), bytes.NewReader(archive), target, RestoreOptions{})
	if !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("Restore without passphrase = %v, want ErrPassphraseRequired", err)
	}
}

func TestNotABackup(t *testing.T) {
	err := Restore(context.Background(), strings.NewReader("hello world, definitely not an archive"), newTarget(t), RestoreOptions{})
	if !errors.Is(err, ErrNotBackup) {
		t.Fatalf("Restore = %v, want ErrNotBackup", err)
	}
}

type tarEntry struct {
	name string
	body []byte
	typ  byte
	link string
}

func readTar(t *testing.T, plain []byte) []tarEntry {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out []tarEntry
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out = append(out, tarEntry{name: h.Name, body: b, typ: h.Typeflag})
	}
}

func writeTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.body)), Typeflag: typ, Linkname: e.link, ModTime: time.Unix(0, 0)}
		if typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil && typ == tar.TypeReg {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestTamperedEntryAbortsAndLeavesTargetUntouched(t *testing.T) {
	dir, _ := seed(t)
	entries := readTar(t, create(t, dir, CreateOptions{NoEncrypt: true}))
	for i := range entries {
		if entries[i].name == "tls/ca.key" {
			entries[i].body = []byte("EVIL") // same size as "KEY"+1? size differs; sha mismatch either way
		}
	}
	target := newTarget(t)
	write(t, filepath.Join(target, "tls", "ca.key"), "ORIGINAL")
	write(t, filepath.Join(target, "bubbles.db"), "olddb")
	err := Restore(context.Background(), bytes.NewReader(writeTar(t, entries)), target, RestoreOptions{Force: true})
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("Restore = %v, want ErrChecksum", err)
	}
	if got := read(t, filepath.Join(target, "tls", "ca.key")); got != "ORIGINAL" {
		t.Errorf("ca.key = %q, target was modified", got)
	}
	if got := read(t, filepath.Join(target, "bubbles.db")); got != "olddb" {
		t.Errorf("bubbles.db = %q, target was modified", got)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".bubbles-restore-*")); len(left) != 0 {
		t.Errorf("staging left behind: %v", left)
	}
}

func TestTamperedEntryIntoFreshTargetCreatesNothing(t *testing.T) {
	dir, _ := seed(t)
	entries := readTar(t, create(t, dir, CreateOptions{NoEncrypt: true}))
	entries[0].body = append(entries[0].body, 0) // the database
	target := newTarget(t)
	if err := Restore(context.Background(), bytes.NewReader(writeTar(t, entries)), target, RestoreOptions{}); !errors.Is(err, ErrChecksum) {
		t.Fatalf("Restore = %v, want ErrChecksum", err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("target dir was left behind")
	}
}

func TestUnsafeEntriesRejected(t *testing.T) {
	dir, _ := seed(t)
	base := readTar(t, create(t, dir, CreateOptions{NoEncrypt: true}))
	cases := map[string]tarEntry{
		"parent":       {name: "files/../../evil", body: []byte("x")},
		"parent-first": {name: "../evil", body: []byte("x")},
		"absolute":     {name: "/etc/passwd", body: []byte("x")},
		"windows-abs":  {name: "C:/evil", body: []byte("x")},
		"backslash":    {name: `files\evil`, body: []byte("x")},
		"unknown-dir":  {name: "other/evil", body: []byte("x")},
		"symlink":      {name: "files/link", typ: tar.TypeSymlink, link: "../../x"},
		"hardlink":     {name: "files/hl", typ: tar.TypeLink, link: "tls/ca.key"},
		"dot":          {name: "files/./x", body: []byte("x")},
		"duplicate":    {name: DBEntry, body: []byte("x")},
	}
	for name, evil := range cases {
		t.Run(name, func(t *testing.T) {
			entries := append([]tarEntry{}, base[:len(base)-1]...)
			entries = append(entries, evil, base[len(base)-1])
			target := newTarget(t)
			err := Restore(context.Background(), bytes.NewReader(writeTar(t, entries)), target, RestoreOptions{})
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Restore = %v, want ErrUnsafePath", err)
			}
			if _, err := os.Stat(target); err == nil {
				t.Error("target was created")
			}
		})
	}
}

func TestMissingManifestRejected(t *testing.T) {
	dir, _ := seed(t)
	entries := readTar(t, create(t, dir, CreateOptions{NoEncrypt: true}))
	entries = entries[:len(entries)-1]
	if err := Restore(context.Background(), bytes.NewReader(writeTar(t, entries)), newTarget(t), RestoreOptions{}); !errors.Is(err, ErrNotBackup) {
		t.Fatalf("Restore = %v, want ErrNotBackup", err)
	}
}

func TestExtraEntryNotInManifestRejected(t *testing.T) {
	dir, _ := seed(t)
	entries := readTar(t, create(t, dir, CreateOptions{NoEncrypt: true}))
	extra := tarEntry{name: "files/extra", body: []byte("x")}
	entries = append(entries[:len(entries)-1], extra, entries[len(entries)-1])
	if err := Restore(context.Background(), bytes.NewReader(writeTar(t, entries)), newTarget(t), RestoreOptions{}); !errors.Is(err, ErrChecksum) {
		t.Fatalf("Restore = %v, want ErrChecksum", err)
	}
}

func TestRestoreRefusesWhenLocked(t *testing.T) {
	dir, _ := seed(t)
	archive := create(t, dir, CreateOptions{NoEncrypt: true})
	target := newTarget(t)
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := config.AcquireLock(target)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	err = Restore(context.Background(), bytes.NewReader(archive), target, RestoreOptions{Force: true})
	if !errors.Is(err, config.ErrLocked) {
		t.Fatalf("Restore = %v, want config.ErrLocked", err)
	}
	if _, err := os.Stat(filepath.Join(target, store.FileName)); err == nil {
		t.Error("database was written while locked")
	}
}

func TestRestoreNonEmptyNeedsForce(t *testing.T) {
	dir, _ := seed(t)
	archive := create(t, dir, CreateOptions{NoEncrypt: true})
	target := newTarget(t)
	write(t, filepath.Join(target, "bubbles.db"), "existing")
	err := Restore(context.Background(), bytes.NewReader(archive), target, RestoreOptions{})
	if !errors.Is(err, ErrNonEmpty) {
		t.Fatalf("Restore = %v, want ErrNonEmpty", err)
	}
	if got := read(t, filepath.Join(target, "bubbles.db")); got != "existing" {
		t.Errorf("existing db changed: %q", got)
	}
	// An empty (or lock-only) directory is fine, and Force replaces content,
	// including a stale WAL that would corrupt the restored database.
	write(t, filepath.Join(target, "bubbles.db-wal"), "stale")
	write(t, filepath.Join(target, "logs", "keep.log"), "log")
	if err := Restore(context.Background(), bytes.NewReader(archive), target, RestoreOptions{Force: true}); err != nil {
		t.Fatalf("Restore --force: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "bubbles.db-wal")); err == nil {
		t.Error("stale WAL survived")
	}
	if got := read(t, filepath.Join(target, "logs", "keep.log")); got != "log" {
		t.Error("unrelated logs were removed")
	}
	if got := read(t, filepath.Join(target, "tls", "ca.crt")); got != "CERT" {
		t.Errorf("ca.crt = %q", got)
	}
}

func TestRestoredPermissions(t *testing.T) {
	dir, _ := seed(t)
	archive := create(t, dir, CreateOptions{NoEncrypt: true})
	target := newTarget(t)
	if err := Restore(context.Background(), bytes.NewReader(archive), target, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if runtimeIsWindows() {
		return
	}
	for _, p := range []string{target, filepath.Join(target, "tls")} {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o700 {
			t.Errorf("%s mode %v, want 0700", p, st.Mode().Perm())
		}
	}
	for _, p := range []string{"bubbles.db", "tls/ca.key", "vapid.json"} {
		if st, _ := os.Stat(filepath.Join(target, p)); st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", p, st.Mode().Perm())
		}
	}
}
