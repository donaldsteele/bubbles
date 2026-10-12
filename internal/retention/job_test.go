package retention

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/files"
	"github.com/donaldsteele/bubbles/internal/files/filestest"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// clock is the fake time shared by the store, the services and the job.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t     *testing.T
	ctx   context.Context
	db    *store.DB
	st    *settings.Store
	chat  *chat.Service
	files *files.Service
	job   *Job
	clock *clock
	dir   string
	admin chat.Actor
}

var day = 24 * time.Hour

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, ctx: context.Background(), dir: t.TempDir(), clock: &clock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}}
	var err error
	e.db, err = store.Open(e.ctx, filepath.Join(e.dir, store.FileName), store.Options{Now: e.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.db.Close() })
	e.st = settings.New(e.db)
	e.chat = chat.New(e.db, e.st)
	e.files, err = files.New(files.Options{
		DB: e.db, Settings: e.st, DataDir: e.dir, Now: e.clock.Now,
		FreeSpace: func(string) (uint64, error) { return 1 << 40, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	e.chat.SetFileAttacher(e.files)
	e.job = New(Options{DB: e.db, Settings: e.st, Blobs: e.files, Now: e.clock.Now})
	e.exec(`INSERT INTO users (id, username, display_name, kind, role, created_at) VALUES ('u1', 'alice', 'Alice', 'human', 'admin', 1)`)
	e.exec(`INSERT INTO users (id, username, display_name, kind, role, created_at) VALUES ('u2', 'bob', 'Bob', 'human', 'member', 1)`)
	e.admin = chat.Actor{ID: "u1", Role: protocol.RoleAdmin}
	return e
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	})
	if err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Read().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// channel creates a public channel with bob in it; retention 0 leaves the
// channel without a retention of its own.
func (e *env) channel(name string, retentionDays int) string {
	e.t.Helper()
	ch, err := e.chat.CreateChannel(e.ctx, e.admin, chat.CreateChannelParams{Name: name, Kind: protocol.ChannelPublic, MemberIDs: []string{"u2"}})
	if err != nil {
		e.t.Fatal(err)
	}
	if retentionDays > 0 {
		e.exec(`UPDATE channels SET retention_days = ? WHERE id = ?`, retentionDays, ch.ID)
	}
	return ch.ID
}

func (e *env) post(channelID, body string, p ...func(*chat.CreateMessageParams)) protocol.Message {
	e.t.Helper()
	params := chat.CreateMessageParams{ChannelID: channelID, Body: body}
	for _, f := range p {
		f(&params)
	}
	m, _, err := e.chat.CreateMessage(e.ctx, e.admin, params)
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func withFiles(ids ...string) func(*chat.CreateMessageParams) {
	return func(p *chat.CreateMessageParams) { p.FileIDs = ids }
}

func inThread(rootID string) func(*chat.CreateMessageParams) {
	return func(p *chat.CreateMessageParams) { p.ThreadRootID = rootID }
}

func (e *env) upload(name string, data []byte) protocol.File {
	e.t.Helper()
	f, err := e.files.Upload(e.ctx, files.Upload{UserID: "u1", Name: name, Body: bytes.NewReader(data)})
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

// blobExists reports whether the blob of f is on disk.
func (e *env) blobExists(f protocol.File) bool {
	e.t.Helper()
	var sha string
	if err := e.db.Read().QueryRow(`SELECT sha256 FROM files WHERE id = ?`, f.ID).Scan(&sha); err != nil {
		e.t.Fatal(err)
	}
	return e.blobShaExists(sha)
}

func (e *env) blobShaExists(sha string) bool {
	_, err := os.Stat(filepath.Join(e.dir, "files", sha[:2], sha[2:4], sha))
	return err == nil
}

func (e *env) sha(f protocol.File) string {
	e.t.Helper()
	var sha string
	if err := e.db.Read().QueryRow(`SELECT sha256 FROM files WHERE id = ?`, f.ID).Scan(&sha); err != nil {
		e.t.Fatal(err)
	}
	return sha
}

func TestRetentionDeletesExpiredMessagesAndOnlyUnreferencedBlobs(t *testing.T) {
	e := newEnv(t)
	alpha := e.channel("alpha", 7) // keeps 7 days
	beta := e.channel("beta", 0)   // keeps everything (no default either)

	// Day 0: uploads and the messages that will expire.
	f1 := e.upload("one.png", filestest.PlainPNG(11, 11))    // attached to an expiring message
	f4 := e.upload("two.png", filestest.PlainPNG(12, 12))    // never attached
	f5 := e.upload("avatar.png", filestest.PlainPNG(13, 13)) // an avatar, never attached
	f6 := e.upload("copy.png", filestest.PlainPNG(15, 15))   // same content as f2, never attached
	e.exec(`UPDATE users SET avatar_file_id = ? WHERE id = 'u1'`, f5.ID)

	old := e.post(alpha, "oldword hello @bob", withFiles(f1.ID))
	if _, err := e.chat.PinMessage(e.ctx, e.admin, alpha, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.chat.AddReaction(e.ctx, e.admin, old.ID, "👍"); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO users (id, username, display_name, kind, role, created_at) VALUES ('bot', 'netdiag', 'netdiag', 'app', 'member', 1)`)
	e.exec(`INSERT INTO apps (id, user_id, name, created_at) VALUES ('app1', 'bot', 'netdiag', 1)`)
	e.exec(`INSERT INTO alerts (message_id, app_id, dedupe_key, severity, title, first_seen_at, last_seen_at) VALUES (?, 'app1', 'k', 'info', 't', 1, 1)`, old.ID)
	betaOld := e.post(beta, "beta oldword")
	if e.count(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'oldword'`) != 2 {
		t.Fatal("the search index does not know the messages")
	}
	if e.count(`SELECT COUNT(*) FROM mentions WHERE message_id = ?`, old.ID) != 1 {
		t.Fatal("the mention was not recorded")
	}

	// A thread whose root is old but whose last reply is recent.
	e.clock.Advance(1 * day)
	root := e.post(alpha, "thread root")
	e.clock.Advance(1 * day)
	e.post(alpha, "old reply", inThread(root.ID))
	e.clock.Advance(7 * day)
	newReply := e.post(alpha, "new reply", inThread(root.ID))

	// Day 10: a fresh message with a file; the same bytes were uploaded on day 0 (f6).
	e.clock.Advance(1 * day)
	content := filestest.PlainPNG(15, 15)
	f2 := e.upload("fresh.png", content)
	if e.sha(f2) != e.sha(f6) {
		t.Fatal("f2 and f6 must share a blob")
	}
	fresh := e.post(alpha, "fresh", withFiles(f2.ID))

	// Day 11, 1 h in: an upload that is not attached yet but younger than 24 h.
	e.clock.Advance(1 * day)
	f3 := e.upload("pending.png", filestest.PlainPNG(17, 17))
	e.clock.Advance(1 * time.Hour)
	shaF1, shaF4 := e.sha(f1), e.sha(f4)

	res, err := e.job.RunOnce(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 2 || res.Files != 3 || res.Blobs != 2 {
		t.Errorf("result = %+v, want 2 messages (the old message and the old reply), 3 files, 2 blobs", res)
	}

	// Old message: gone with everything that depended on it.
	if e.count(`SELECT COUNT(*) FROM messages WHERE id = ?`, old.ID) != 0 {
		t.Error("the expired message is still there")
	}
	for _, table := range []string{"attachments", "reactions", "mentions", "pins", "alerts"} {
		if n := e.count(`SELECT COUNT(*) FROM `+table+` WHERE message_id = ?`, old.ID); n != 0 {
			t.Errorf("%d %s rows of the expired message remain", n, table)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE json_extract(payload, '$.id') = ? OR json_extract(payload, '$.message_id') = ?`, old.ID, old.ID); n != 0 {
		t.Errorf("%d events still name the expired message", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE json_extract(payload, '$.id') = ?`, fresh.ID); n == 0 {
		t.Error("the events of a message that is kept were deleted")
	}
	if e.count(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'oldword'`) != 1 {
		t.Error("the search index still finds the expired message (or lost the other channel's)")
	}
	// The thread root stays for its live reply; the old reply is gone and the count is refreshed.
	if e.count(`SELECT COUNT(*) FROM messages WHERE id IN (?, ?, ?)`, root.ID, newReply.ID, fresh.ID) != 3 {
		t.Error("messages that are still within retention were deleted")
	}
	if n := e.count(`SELECT reply_count FROM messages WHERE id = ?`, root.ID); n != 1 {
		t.Errorf("root reply_count = %d, want 1", n)
	}
	if e.count(`SELECT COUNT(*) FROM messages WHERE body = 'old reply'`) != 0 {
		t.Error("the expired reply is still there")
	}
	// The channel without retention keeps its old message.
	if e.count(`SELECT COUNT(*) FROM messages WHERE id = ?`, betaOld.ID) != 1 {
		t.Error("a message of a channel without retention was deleted")
	}

	// Files: unattached old uploads go, attached ones, avatars and young uploads stay.
	for name, f := range map[string]protocol.File{"f1": f1, "f4": f4, "f6": f6} {
		if e.count(`SELECT COUNT(*) FROM files WHERE id = ?`, f.ID) != 0 {
			t.Errorf("%s: the row survived", name)
		}
	}
	for name, f := range map[string]protocol.File{"f2": f2, "f3": f3, "f5": f5} {
		if e.count(`SELECT COUNT(*) FROM files WHERE id = ?`, f.ID) != 1 {
			t.Errorf("%s: the row was deleted", name)
		}
	}
	if e.blobShaExists(shaF1) || e.blobShaExists(shaF4) {
		t.Error("blobs without a file row are still on disk")
	}
	for name, f := range map[string]protocol.File{"f2 (shared with the deleted f6)": f2, "f3": f3, "f5": f5} {
		if !e.blobExists(f) {
			t.Errorf("%s: the blob was removed although a row refers to it", name)
		}
	}

	// A second run finds nothing to do.
	if res, err := e.job.RunOnce(e.ctx); err != nil || res != (Result{}) {
		t.Errorf("second run = %+v, %v", res, err)
	}
}

func TestRetentionUsesTheDefaultAndExpiresThreadsFromTheEnd(t *testing.T) {
	e := newEnv(t)
	alpha := e.channel("alpha", 7)
	beta := e.channel("beta", 0)
	if err := e.st.Set(e.ctx, audit.System, settings.KeyDefaultRetentionDays, 30); err != nil {
		t.Fatal(err)
	}
	f := e.upload("a.png", filestest.PlainPNG(20, 20))
	sha := e.sha(f)
	e.post(alpha, "a message", withFiles(f.ID))
	root := e.post(alpha, "root")
	e.post(alpha, "reply one", inThread(root.ID))
	e.post(alpha, "reply two", inThread(root.ID))
	e.post(beta, "beta message")
	e.clock.Advance(20 * day)
	betaYoung := e.post(beta, "beta young")
	e.clock.Advance(15 * day) // day 35: alpha's 7 days and beta's default 30 days are over for the first posts

	if _, err := e.job.RunOnce(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE channel_id = ?`, alpha); n != 0 {
		t.Errorf("%d messages left in alpha: a thread must expire reply by reply, then its root", n)
	}
	var left []string
	rows, err := e.db.Read().Query(`SELECT id FROM messages WHERE channel_id = ?`, beta)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if len(left) != 1 || left[0] != betaYoung.ID {
		t.Errorf("beta keeps %v, want only the young message %s (the default is 30 days)", left, betaYoung.ID)
	}
	if e.blobShaExists(sha) || e.count(`SELECT COUNT(*) FROM files`) != 0 {
		t.Error("the file of the expired message was kept")
	}

	// Without a default, channels without a retention of their own keep everything.
	if err := e.st.Set(e.ctx, audit.System, settings.KeyDefaultRetentionDays, nil); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(400 * day)
	if res, err := e.job.RunOnce(e.ctx); err != nil || res.Messages != 0 {
		t.Errorf("without any retention: %+v, %v", res, err)
	}
}

func TestRetentionDeletesInBatchesOfFiveHundred(t *testing.T) {
	e := newEnv(t)
	ch := e.channel("alpha", 1)
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		for i := range 1200 {
			_, err := tx.Exec(`INSERT INTO messages (id, channel_id, author_id, kind, body, body_plain, created_at)
				VALUES (?, ?, 'u1', 'text', 'x', 'x', ?)`, fmt.Sprintf("M%05d", i), ch, 1000+int64(i))
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cutoff := e.clock.Now().UnixMilli()
	for i, want := range []int{BatchSize, BatchSize, 200, 0} {
		n, err := e.job.deleteMessageBatch(e.ctx, ch, cutoff)
		if err != nil || n != want {
			t.Fatalf("batch %d deleted %d (%v), want %d", i, n, err, want)
		}
	}
	// The whole job does the same in one call.
	if err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO messages (id, channel_id, author_id, kind, body, body_plain, created_at) VALUES ('Z1', ?, 'u1', 'text', 'y', 'y', 5)`, ch)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if res, err := e.job.RunOnce(e.ctx); err != nil || res.Messages != 1 {
		t.Errorf("RunOnce = %+v, %v", res, err)
	}
}

func TestRetentionOrphanFilesNeedToBeOlderThan24Hours(t *testing.T) {
	e := newEnv(t)
	old := e.upload("old.png", filestest.PlainPNG(21, 21))
	e.clock.Advance(25 * time.Hour)
	young := e.upload("young.png", filestest.PlainPNG(22, 22))
	e.clock.Advance(23 * time.Hour)
	// old is 48 h, young 23 h.
	res, err := e.job.RunOnce(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 || res.Blobs != 1 || res.Messages != 0 {
		t.Errorf("result = %+v", res)
	}
	if e.count(`SELECT COUNT(*) FROM files WHERE id = ?`, old.ID) != 0 || e.count(`SELECT COUNT(*) FROM files WHERE id = ?`, young.ID) != 1 {
		t.Error("the wrong file was deleted")
	}
	if !e.blobExists(young) {
		t.Error("the young file lost its blob")
	}
}

func TestRetentionSweepsStaleSpoolFiles(t *testing.T) {
	e := newEnv(t)
	tmp := filepath.Join(e.dir, "tmp")
	stale := filepath.Join(tmp, "up-stale")
	fresh := filepath.Join(tmp, "up-fresh")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("partial upload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, past, past); err != nil {
		t.Fatal(err)
	}
	// File times are real ones, so the sweep must use the real clock.
	var err error
	e.files, err = files.New(files.Options{DB: e.db, Settings: e.st, DataDir: e.dir, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	e.job = New(Options{DB: e.db, Settings: e.st, Blobs: e.files, Now: e.clock.Now})
	res, err := e.job.RunOnce(e.ctx)
	if err != nil || res.TmpFiles != 1 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("the stale spool file is still there")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a fresh spool file (an upload in flight) was removed")
	}
}
