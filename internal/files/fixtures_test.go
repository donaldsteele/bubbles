package files

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// testEnv is a files service over a temp data directory.
type testEnv struct {
	t       *testing.T
	ctx     context.Context
	db      *store.DB
	st      *settings.Store
	svc     *Service
	dataDir string
	free    atomic.Uint64
	clock   *time.Time
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	e := &testEnv{t: t, ctx: context.Background(), dataDir: dir, clock: &now}
	var err error
	e.db, err = store.Open(e.ctx, filepath.Join(dir, store.FileName), store.Options{Now: func() time.Time { return *e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.db.Close() })
	e.st = settings.New(e.db)
	e.free.Store(1 << 40)
	e.svc, err = New(Options{
		DB: e.db, Settings: e.st, DataDir: dir,
		FreeSpace: func(string) (uint64, error) { return e.free.Load(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	e.addUser("u1")
	e.addUser("u2")
	return e
}

func (e *testEnv) addUser(id string) {
	e.t.Helper()
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO users (id, username, display_name, kind, role, created_at) VALUES (?, ?, ?, 'human', 'member', ?)`,
			id, id, id, tx.Now())
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) set(key string, value any) {
	e.t.Helper()
	if err := e.st.Set(e.ctx, audit.System, key, value); err != nil {
		e.t.Fatal(err)
	}
}

// upload stores data as name for user and fails the test on error.
func (e *testEnv) upload(user, name string, data []byte) protocol.File {
	e.t.Helper()
	f, err := e.tryUpload(user, name, data)
	if err != nil {
		e.t.Fatalf("upload %q: %v", name, err)
	}
	return f
}

func (e *testEnv) tryUpload(user, name string, data []byte) (protocol.File, error) {
	return e.svc.Upload(e.ctx, Upload{UserID: user, Name: name, Body: bytes.NewReader(data)})
}

// row loads the files row of f.
func (e *testEnv) row(f protocol.File) fileRow {
	e.t.Helper()
	r, err := scanFile(e.db.Read().QueryRow(`SELECT `+fileCols+` FROM files WHERE id = ?`, f.ID))
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// blob returns the stored bytes of f.
func (e *testEnv) blob(f protocol.File) []byte {
	e.t.Helper()
	b, err := os.ReadFile(e.svc.blobPath(e.row(f).SHA))
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

// thumb returns the stored thumbnail of f.
func (e *testEnv) thumb(f protocol.File) []byte {
	e.t.Helper()
	b, err := os.ReadFile(e.svc.blobPath(e.row(f).SHA) + thumbSuffix)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

// blobCount counts the blobs in the content-addressed store (not thumbnails).
func (e *testEnv) blobCount() int {
	e.t.Helper()
	n := 0
	err := filepath.WalkDir(e.svc.filesDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasSuffix(p, thumbSuffix) {
			n++
		}
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

// tmpEntries counts the entries left in the upload spool.
func (e *testEnv) tmpEntries() int {
	e.t.Helper()
	entries, err := os.ReadDir(e.svc.tmpDir)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(entries)
}

func noSecrets(t *testing.T, what string, data []byte, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if bytes.Contains(data, []byte(s)) {
			t.Errorf("%s still contains %q", what, s)
		}
	}
}
