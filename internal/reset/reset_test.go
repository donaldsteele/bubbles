package reset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/push"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/internal/tlsca"
)

// canary is planted in a message body and in every blob; after a reset it
// must not appear in any file under the data directory.
const canary = "zebra-canary-7731"

var errCrash = errors.New("injected crash")

// fixture is a data directory with content of every kind.
type fixture struct {
	dir        string
	instanceID string
	vapid      []byte
	caCert     []byte
}

// seed builds a data directory with users, a session, an app and its token,
// a channel, messages (a thread reply and an alert among them), mentions,
// reactions, pins, files and blobs, attachments, events, settings, audit
// rows, logs, backups, spool files, VAPID keys, an admin password file and a CA.
func seed(t *testing.T, blobs int) fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	if err := config.EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, filepath.Join(dir, store.FileName), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	shas := make([]string, blobs)
	for i := range shas {
		shas[i] = fmt.Sprintf("%064x", i+1)
	}
	err = db.Do(ctx, func(tx *store.Tx) error {
		now := tx.Now()
		stmts := []struct {
			q    string
			args []any
		}{
			{`INSERT INTO users (id, username, kind, role, pw_hash, created_at) VALUES ('u1', 'admin', 'human', 'admin', 'x', ?)`, []any{now}},
			{`INSERT INTO users (id, username, kind, role, pw_hash, created_at) VALUES ('u2', 'mia', 'human', 'member', 'x', ?)`, []any{now}},
			{`INSERT INTO users (id, username, kind, role, created_at) VALUES ('b1', 'netdiag', 'app', 'member', ?)`, []any{now}},
			{`INSERT INTO sessions (id_hash, user_id, kind, created_at, last_seen_at, idle_expires_at, abs_expires_at) VALUES (X'01', 'u1', 'web', ?, ?, ?, ?)`, []any{now, now, now + 1e9, now + 1e9}},
			{`INSERT INTO apps (id, user_id, name, created_at) VALUES ('a1', 'b1', 'netdiag', ?)`, []any{now}},
			{`INSERT INTO app_tokens (id, app_id, token_hash, prefix, created_at) VALUES ('t1', 'a1', X'02', 'bub_abcd', ?)`, []any{now}},
			{`INSERT INTO channels (id, kind, name, created_at) VALUES ('c1', 'public', 'general', ?)`, []any{now}},
			{`INSERT INTO memberships (channel_id, user_id, joined_at, last_read_id) VALUES ('c1', 'u1', ?, 'm2')`, []any{now}},
			{`INSERT INTO memberships (channel_id, user_id, joined_at) VALUES ('c1', 'u2', ?)`, []any{now}},
			{`INSERT INTO messages (id, channel_id, author_id, body, body_plain, created_at) VALUES ('m1', 'c1', 'u1', ?, ?, ?)`, []any{canary, canary, now}},
			{`INSERT INTO messages (id, channel_id, author_id, thread_root_id, body, body_plain, created_at) VALUES ('m2', 'c1', 'u2', 'm1', ?, ?, ?)`, []any{"re " + canary, "re " + canary, now}},
			{`INSERT INTO messages (id, channel_id, author_id, kind, body, body_plain, created_at) VALUES ('m3', 'c1', 'b1', 'alert', ?, ?, ?)`, []any{canary, canary, now}},
			{`INSERT INTO messages (id, channel_id, author_id, kind, body, body_plain, created_at) VALUES ('m4', 'c1', 'u1', 'system', 'joined', 'joined', ?)`, []any{now}},
			{`INSERT INTO mentions (message_id, user_id) VALUES ('m1', 'u2')`, nil},
			{`INSERT INTO reactions (message_id, user_id, emoji, created_at) VALUES ('m1', 'u2', '👍', ?)`, []any{now}},
			{`INSERT INTO pins (channel_id, message_id, pinned_by, pinned_at) VALUES ('c1', 'm1', 'u1', ?)`, []any{now}},
			{`INSERT INTO alerts (message_id, app_id, dedupe_key, severity, title, first_seen_at, last_seen_at) VALUES ('m3', 'a1', 'printer', 'warning', ?, ?, ?)`, []any{canary, now, now}},
			{`INSERT INTO events (type, channel_id, payload, created_at) VALUES ('message.created', 'c1', ?, ?)`, []any{`{"body":"` + canary + `"}`, now}},
			{`INSERT INTO settings (key, value) VALUES ('instance_name', '"Party"'), ('mdns_enabled', 'false'), ('not_a_setting', '1')`, nil},
		}
		for _, s := range stmts {
			if _, err := tx.Exec(s.q, s.args...); err != nil {
				return fmt.Errorf("%s: %w", s.q, err)
			}
		}
		for i, sha := range shas {
			id := fmt.Sprintf("f%d", i)
			if _, err := tx.Exec(`INSERT INTO files (id, sha256, size, mime, category, original_name, uploader_id, created_at)
				VALUES (?, ?, ?, 'image/png', 'image', ?, 'u1', ?)`, id, sha, len(canary), canary+".png", now); err != nil {
				return err
			}
		}
		if blobs > 0 {
			if _, err := tx.Exec(`INSERT INTO attachments (message_id, file_id) VALUES ('m1', 'f0')`); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE users SET avatar_file_id = 'f0' WHERE id = 'u2'`); err != nil {
				return err
			}
		}
		_, err := audit.Write(tx, audit.Console.Entry("setup.complete", "user", "u1", nil))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	f.dir = dir
	if err := db.Read().QueryRow(`SELECT id FROM instance`).Scan(&f.instanceID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, sha := range shas {
		p := filepath.Join(dir, config.DirFiles, sha[0:2], sha[2:4], sha)
		writeFile(t, p, canary)
	}
	writeFile(t, filepath.Join(dir, config.DirLogs, "bubbles.log"), `{"msg":"starting"}`)
	writeFile(t, filepath.Join(dir, config.DirLogs, "bubbles.log.1"), `{"msg":"old"}`)
	writeFile(t, filepath.Join(dir, config.DirBackups, store.SnapshotPrefix+"1-2.db"), canary)
	writeFile(t, filepath.Join(dir, config.DirBackups, "manual.bbk"), "archive")
	writeFile(t, filepath.Join(dir, config.DirTmp, "upload-1"), canary)
	if _, err := push.LoadOrCreateVAPID(dir); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, auth.AdminPasswordFile), "password: stale")
	f.vapid = readFile(t, filepath.Join(dir, push.VAPIDFile))
	ca, err := tlsca.NewCA(tlsca.CAOptions{InstanceName: "party", Hostname: "host"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ca.Save(filepath.Join(dir, config.DirTLS)); err != nil {
		t.Fatal(err)
	}
	f.caCert = readFile(t, filepath.Join(dir, config.DirTLS, tlsca.CACertFile))
	return f
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func openTestDB(t *testing.T, dir string) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(dir, store.FileName), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, db *store.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Read().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

// sweepAndScan deletes the trash, then fails if the canary is in any file
// under dir.
func sweepAndScan(t *testing.T, dir string) {
	t.Helper()
	if _, err := SweepTrash(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range entries(t, dir) {
		if strings.HasPrefix(name, TrashPrefix) {
			t.Errorf("trash %s left after the sweep", name)
		}
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(canary)) {
			t.Errorf("canary survives in %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// checkContentCleared asserts the state after clear content.
func checkContentCleared(t *testing.T, f fixture) {
	t.Helper()
	if m, err := ReadMarker(f.dir); err != nil || m != nil {
		t.Fatalf("marker after reset: %+v, %v", m, err)
	}
	db := openTestDB(t, f.dir)
	for _, table := range []string{"messages", "files", "alerts", "events", "reactions", "pins", "mentions", "attachments", "messages_fts_docsize"} {
		if n := count(t, db, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s: %d rows left", table, n)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?`, `"`+canary+`"`); n != 0 {
		t.Errorf("search still finds %d messages", n)
	}
	for table, want := range map[string]int{"users": 3, "sessions": 1, "apps": 1, "app_tokens": 1, "channels": 1, "memberships": 2, "settings": 3} {
		if n := count(t, db, `SELECT count(*) FROM `+table); n != want {
			t.Errorf("%s: %d rows, want %d (kept)", table, n, want)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM memberships WHERE last_read_id IS NOT NULL`); n != 0 {
		t.Errorf("%d memberships keep a read position", n)
	}
	if n := count(t, db, `SELECT count(*) FROM users WHERE avatar_file_id IS NOT NULL`); n != 0 {
		t.Errorf("%d avatars point at deleted files", n)
	}
	var id string
	var epoch int64
	if err := db.Read().QueryRow(`SELECT id, epoch FROM instance`).Scan(&id, &epoch); err != nil || id != f.instanceID || epoch != 2 {
		t.Errorf("instance = %s epoch %d (%v), want %s epoch 2", id, epoch, err, f.instanceID)
	}
	if n := count(t, db, `SELECT count(*) FROM audit_log WHERE action = ?`, ActionContentCleared); n != 1 {
		t.Errorf("%d content_cleared audit rows", n)
	}
	db.Close()
	if names := entries(t, filepath.Join(f.dir, config.DirFiles)); len(names) != 0 {
		t.Errorf("files/ holds %v", names)
	}
	if names := entries(t, filepath.Join(f.dir, config.DirBackups)); strings.Join(names, ",") != "manual.bbk" {
		t.Errorf("backups/ = %v, want only the manual backup", names)
	}
	if names := entries(t, filepath.Join(f.dir, config.DirTmp)); len(names) != 0 {
		t.Errorf("tmp/ holds %v", names)
	}
	if !bytes.Equal(readFile(t, filepath.Join(f.dir, push.VAPIDFile)), f.vapid) {
		t.Error("VAPID keys changed")
	}
	sweepAndScan(t, f.dir)
}

// checkFactory asserts the state after a factory reset.
func checkFactory(t *testing.T, f fixture, res *Result, rotate, keep bool) {
	t.Helper()
	if m, err := ReadMarker(f.dir); err != nil || m != nil {
		t.Fatalf("marker after reset: %+v, %v", m, err)
	}
	db := openTestDB(t, f.dir)
	var id string
	var epoch int64
	if err := db.Read().QueryRow(`SELECT id, epoch FROM instance`).Scan(&id, &epoch); err != nil || id == f.instanceID || epoch != 1 {
		t.Errorf("instance = %s epoch %d (%v), want a new id", id, epoch, err)
	}
	for _, table := range []string{"users", "sessions", "apps", "app_tokens", "channels", "memberships", "messages", "files", "events", "alerts", "messages_fts_docsize"} {
		if n := count(t, db, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s: %d rows left", table, n)
		}
	}
	var action, kind, detail string
	var actorID *string
	if err := db.Read().QueryRow(`SELECT action, actor_kind, actor_id, detail FROM audit_log`).Scan(&action, &kind, &actorID, &detail); err != nil {
		t.Fatalf("audit log: %v", err)
	}
	if n := count(t, db, `SELECT count(*) FROM audit_log`); n != 1 || action != ActionFactoryReset || actorID != nil {
		t.Errorf("audit log: %d rows, first %s by %v", n, action, actorID)
	}
	var d map[string]bool
	if err := json.Unmarshal([]byte(detail), &d); err != nil || d["rotate_ca"] != rotate || d["keep_settings"] != keep || len(d) != 2 {
		t.Errorf("audit detail = %s", detail)
	}
	wantSettings := 0
	if keep {
		wantSettings = 2
		var name string
		if err := db.Read().QueryRow(`SELECT value FROM settings WHERE key = 'instance_name'`).Scan(&name); err != nil || name != `"Party"` {
			t.Errorf("kept instance_name = %s, %v", name, err)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM settings`); n != wantSettings {
		t.Errorf("settings: %d rows, want %d", n, wantSettings)
	}
	if n := count(t, db, `SELECT count(*) FROM users`); n != 0 {
		t.Errorf("%d users after the reset, want none (the server bootstraps the admin)", n)
	}
	db.Close()
	if bytes.Equal(readFile(t, filepath.Join(f.dir, push.VAPIDFile)), f.vapid) {
		t.Error("VAPID keys were not replaced")
	}
	if _, err := os.Stat(filepath.Join(f.dir, auth.AdminPasswordFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("admin password file survived: %v", err)
	}
	for _, sub := range []string{config.DirLogs, config.DirBackups, config.DirTmp, config.DirFiles} {
		if names := entries(t, filepath.Join(f.dir, sub)); len(names) != 0 {
			t.Errorf("%s/ holds %v", sub, names)
		}
	}
	tlsNames := entries(t, filepath.Join(f.dir, config.DirTLS))
	if rotate {
		if len(tlsNames) != 0 {
			t.Errorf("tls/ = %v after rotation without a server", tlsNames)
		}
	} else if !bytes.Equal(readFile(t, filepath.Join(f.dir, config.DirTLS, tlsca.CACertFile)), f.caCert) {
		t.Error("CA changed without rotate_ca")
	}
	sweepAndScan(t, f.dir)
}

func TestResetClearContent(t *testing.T) {
	f := seed(t, 3)
	res, err := Run(context.Background(), f.dir, Request{Mode: ModeContent, Actor: audit.User("u1", "192.168.1.2")}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeContent || res.Epoch != 2 {
		t.Errorf("result = %+v", res)
	}
	checkContentCleared(t, f)
	db := openTestDB(t, f.dir)
	var actor string
	if err := db.Read().QueryRow(`SELECT actor_id FROM audit_log WHERE action = ?`, ActionContentCleared).Scan(&actor); err != nil || actor != "u1" {
		t.Errorf("content audit actor = %q, %v", actor, err)
	}
}

func TestResetFactory(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rotate, keep bool
	}{
		{"defaults", false, false},
		{"rotate CA", true, false},
		{"keep settings", false, true},
		{"rotate and keep", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := seed(t, 2)
			res, err := Run(context.Background(), f.dir, Request{
				Mode: ModeFactory, RotateCA: tc.rotate, KeepSettings: tc.keep, Actor: audit.User("u1", "192.168.1.2"),
			}, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Mode != ModeFactory || res.CARotated != tc.rotate {
				t.Errorf("result = %+v", res)
			}
			checkFactory(t, f, res, tc.rotate, tc.keep)
		})
	}
}

// TestResetHooksRunInPipelineOrder checks where a live server's hooks run
// relative to the data steps.
func TestResetHooksRunInPipelineOrder(t *testing.T) {
	for _, mode := range []Mode{ModeContent, ModeFactory} {
		t.Run(string(mode), func(t *testing.T) {
			f := seed(t, 1)
			var live *store.DB
			if mode == ModeContent {
				live = openTestDB(t, f.dir)
			}
			var got []string
			note := func(s string) func(context.Context) error {
				return func(context.Context) error { got = append(got, s); return nil }
			}
			h := Hooks{
				DB: live, Quiesce: note("quiesce"), Release: note("release"), Restart: note("restart"),
				ReopenLogs: func(clear func() error) error { got = append(got, "logs"); return clear() },
				NewCA:      func() error { got = append(got, "ca"); return nil },
			}
			if _, err := Run(context.Background(), f.dir, Request{Mode: mode, RotateCA: mode == ModeFactory}, h); err != nil {
				t.Fatal(err)
			}
			want := "quiesce,restart"
			if mode == ModeFactory {
				want = "quiesce,release,logs,ca,restart"
			}
			if strings.Join(got, ",") != want {
				t.Errorf("hooks ran %v, want %s", got, want)
			}
			if mode == ModeContent {
				// The live database stays open and usable.
				if n := count(t, live, `SELECT count(*) FROM messages`); n != 0 {
					t.Errorf("live database sees %d messages", n)
				}
			}
		})
	}
}

// TestResetCrashAfterEveryStepThenRecover kills the reset after every step
// (before and after the marker records it) and checks that Recover
// completes it.
func TestResetCrashAfterEveryStepThenRecover(t *testing.T) {
	for _, req := range []Request{
		{Mode: ModeContent},
		{Mode: ModeFactory, RotateCA: true, KeepSettings: true},
	} {
		r := &runner{m: &Marker{Mode: req.Mode, RotateCA: req.RotateCA, KeepSettings: req.KeepSettings}}
		for _, st := range r.steps() {
			for _, recorded := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/recorded=%v", req.Mode, st.name, recorded), func(t *testing.T) {
					f := seed(t, 2)
					crash := func(step string, rec bool) error {
						if step == st.name && rec == recorded {
							return errCrash
						}
						return nil
					}
					if _, err := Run(context.Background(), f.dir, req, Hooks{crash: crash}); !errors.Is(err, errCrash) {
						t.Fatalf("Run = %v, want the injected crash", err)
					}
					m, err := ReadMarker(f.dir)
					if err != nil || m == nil {
						t.Fatalf("marker after the crash: %+v, %v", m, err)
					}
					if _, err := Run(context.Background(), f.dir, req, Hooks{}); !errors.Is(err, ErrPending) {
						t.Fatalf("second Run = %v, want ErrPending", err)
					}
					res, err := Recover(context.Background(), f.dir, RecoverOptions{})
					if err != nil || res == nil {
						t.Fatalf("Recover = %+v, %v", res, err)
					}
					if req.Mode == ModeContent {
						checkContentCleared(t, f)
					} else {
						checkFactory(t, f, res, true, true)
					}
					if res, err := Recover(context.Background(), f.dir, RecoverOptions{}); res != nil || err != nil {
						t.Errorf("second Recover = %+v, %v", res, err)
					}
				})
			}
		}
	}
}

func TestResetRecoverWithoutMarkerIsNoop(t *testing.T) {
	dir := t.TempDir()
	if res, err := Recover(context.Background(), dir, RecoverOptions{}); res != nil || err != nil {
		t.Fatalf("Recover = %+v, %v", res, err)
	}
	if names := entries(t, dir); len(names) != 0 {
		t.Errorf("Recover created %v", names)
	}
}

func TestResetMarkerFile(t *testing.T) {
	dir := t.TempDir()
	m := &Marker{ID: "01J", Mode: ModeFactory, KeepSettings: true, Settings: map[string]json.RawMessage{"instance_name": json.RawMessage(`"x"`)}, Done: []string{StepQuiesce}}
	if err := WriteMarker(dir, m); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMarker(dir)
	if err != nil || got == nil || got.Mode != ModeFactory || !got.KeepSettings || string(got.Settings["instance_name"]) != `"x"` || !got.done(StepQuiesce) {
		t.Fatalf("ReadMarker = %+v, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(dir, MarkerFile)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("marker mode = %v, %v", fi.Mode().Perm(), err)
		}
	}
	writeFile(t, filepath.Join(dir, MarkerFile), `{"version":1,"mode":"everything"}`)
	if _, err := ReadMarker(dir); err == nil {
		t.Error("ReadMarker accepted an unknown mode")
	}
	if err := RemoveMarker(dir); err != nil {
		t.Fatal(err)
	}
	if err := RemoveMarker(dir); err != nil {
		t.Errorf("second RemoveMarker: %v", err)
	}
}

func TestResetSweepTrash(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, config.DirFiles, "ab", "cd", "blob"), "x")
	if err := TrashFiles(dir); err != nil {
		t.Fatal(err)
	}
	if err := TrashFiles(dir); err != nil { // an empty files/ is moved too
		t.Fatal(err)
	}
	var trash int
	for _, name := range entries(t, dir) {
		if strings.HasPrefix(name, TrashPrefix) {
			trash++
		}
	}
	if trash != 2 || len(entries(t, filepath.Join(dir, config.DirFiles))) != 0 {
		t.Fatalf("after TrashFiles: %v", entries(t, dir))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SweepTrash(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled sweep = %v", err)
	}
	n, err := SweepTrash(context.Background(), dir)
	if err != nil || n != 2 {
		t.Fatalf("SweepTrash = %d, %v", n, err)
	}
	if names := entries(t, dir); strings.Join(names, ",") != config.DirFiles {
		t.Errorf("left %v", names)
	}
}

func TestResetPreviewAndDeniedAudit(t *testing.T) {
	f := seed(t, 3)
	db := openTestDB(t, f.dir)
	p, err := Preview(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if p.Users != 2 || p.Channels != 1 || p.Messages != 4 || p.Files != 3 || p.Bytes != int64(3*len(canary)) {
		t.Errorf("preview = %+v", p)
	}
	if err := AuditDenied(context.Background(), db, audit.User("u1", "192.168.1.2"), ModeFactory, "password"); err != nil {
		t.Fatal(err)
	}
	var detail string
	if err := db.Read().QueryRow(`SELECT detail FROM audit_log WHERE action = ?`, ActionResetDenied).Scan(&detail); err != nil ||
		detail != `{"mode":"factory","reason":"password"}` {
		t.Errorf("denied audit detail = %s, %v", detail, err)
	}
}

// TestResetWith2000FilesIsFast: the files are moved aside, not deleted, so
// the reset takes the same time whatever the volume.
func TestResetWith2000FilesIsFast(t *testing.T) {
	for _, mode := range []Mode{ModeContent, ModeFactory} {
		t.Run(string(mode), func(t *testing.T) {
			f := seed(t, 2000)
			start := time.Now()
			if _, err := Run(context.Background(), f.dir, Request{Mode: mode}, Hooks{}); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d > resetSpeedLimit {
				t.Errorf("reset took %v", d)
			}
			if n, err := SweepTrash(context.Background(), f.dir); err != nil || n != 1 {
				t.Errorf("SweepTrash = %d, %v", n, err)
			}
		})
	}
}
