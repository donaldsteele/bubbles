package store

import (
	"context"
	"strings"
	"testing"
)

// fixture inserts rows through the writer and fails the test on error.
func fixture(t *testing.T, db *DB, query string, args ...any) {
	t.Helper()
	err := db.Do(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	})
	if err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
}

// execErr runs one statement through the writer and returns its error.
func execErr(db *DB, query string, args ...any) error {
	return db.Do(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	})
}

// seedBasics creates user u1, app a1 (bot user bot1) and channels c1 (public)
// and d1 (dm).
func seedBasics(t *testing.T, db *DB) {
	t.Helper()
	fixture(t, db, `INSERT INTO users (id, username, kind, role, created_at) VALUES
		('u1', 'alice', 'human', 'admin', 1), ('bot1', 'monitor', 'app', 'member', 1)`)
	fixture(t, db, `INSERT INTO apps (id, user_id, name, created_at) VALUES ('a1', 'bot1', 'Monitor', 1)`)
	fixture(t, db, `INSERT INTO channels (id, kind, name, created_at) VALUES ('c1', 'public', 'general', 1)`)
	fixture(t, db, `INSERT INTO channels (id, kind, dm_key, created_at) VALUES ('d1', 'dm', 'bot1,u1', 1)`)
}

func insertMessage(t *testing.T, db *DB, id, body string) {
	t.Helper()
	fixture(t, db, `INSERT INTO messages (id, channel_id, author_id, body, body_plain, created_at)
		VALUES (?, 'c1', 'u1', ?, ?, 1)`, id, body, body)
}

func ftsMatches(t *testing.T, db *DB, query string) []string {
	t.Helper()
	rows, err := db.Read().Query(`SELECT m.id FROM messages_fts f JOIN messages m ON m.rowid = f.rowid
		WHERE messages_fts MATCH ? ORDER BY m.id`, query)
	if err != nil {
		t.Fatalf("MATCH %q: %v", query, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func ftsIntegrity(t *testing.T, db *DB) {
	t.Helper()
	// rank = 1 also verifies the index against the content table.
	if err := execErr(db, `INSERT INTO messages_fts (messages_fts, rank) VALUES ('integrity-check', 1)`); err != nil {
		t.Fatalf("FTS integrity-check: %v", err)
	}
}

func TestFTSMatchAndSync(t *testing.T) {
	db := openTestDB(t, Options{})
	seedBasics(t, db)
	insertMessage(t, db, "m1", "Café au lait is ready")
	insertMessage(t, db, "m2", "deploy finished on prod")

	steps := []struct {
		name  string
		apply string
		query string
		want  string
	}{
		{"insert indexed", "", "deploy", "m2"},
		{"diacritics folded", "", "cafe", "m1"},
		{"update removes old terms", `UPDATE messages SET body_plain = 'rollback started' WHERE id = 'm2'`, "deploy", ""},
		{"update indexes new terms", "", "rollback", "m2"},
		{"non-body update keeps index", `UPDATE messages SET reply_count = 3 WHERE id = 'm2'`, "rollback", "m2"},
		{"delete removes terms", `DELETE FROM messages WHERE id = 'm1'`, "cafe", ""},
	}
	for _, s := range steps {
		if s.apply != "" {
			fixture(t, db, s.apply)
		}
		got := strings.Join(ftsMatches(t, db, s.query), ",")
		if got != s.want {
			t.Errorf("%s: MATCH %q = %q, want %q", s.name, s.query, got, s.want)
		}
		ftsIntegrity(t, db)
	}
}

func TestFTSThreadCascade(t *testing.T) {
	db := openTestDB(t, Options{})
	seedBasics(t, db)
	insertMessage(t, db, "root", "incident thread")
	fixture(t, db, `INSERT INTO messages (id, channel_id, author_id, thread_root_id, body, body_plain, created_at)
		VALUES ('reply', 'c1', 'u1', 'root', 'incident reply', 'incident reply', 2)`)
	fixture(t, db, `DELETE FROM messages WHERE id = 'root'`)

	var n int
	if err := db.Read().QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("messages after root delete = %d (err %v), want 0", n, err)
	}
	if got := ftsMatches(t, db, "incident"); len(got) != 0 {
		t.Errorf("MATCH after cascade = %v, want none", got)
	}
	ftsIntegrity(t, db)
}

func TestAlertsPartialUniqueIndex(t *testing.T) {
	db := openTestDB(t, Options{})
	seedBasics(t, db)
	for _, id := range []string{"m1", "m2", "m3", "m4"} {
		insertMessage(t, db, id, "alert")
	}
	insertAlert := func(msg, state string) error {
		return execErr(db, `INSERT INTO alerts (message_id, app_id, dedupe_key, severity, state, title, first_seen_at, last_seen_at)
			VALUES (?, 'a1', 'disk-full', 'critical', ?, 'Disk full', 1, 1)`, msg, state)
	}

	if err := insertAlert("m1", "open"); err != nil {
		t.Fatalf("first open alert: %v", err)
	}
	if err := insertAlert("m2", "open"); err == nil {
		t.Error("second open alert with same key accepted, want UNIQUE violation")
	}
	if err := insertAlert("m2", "acked"); err == nil {
		t.Error("acked alert with same key as open accepted, want UNIQUE violation")
	}
	fixture(t, db, `UPDATE alerts SET state = 'resolved' WHERE message_id = 'm1'`)
	if err := insertAlert("m2", "open"); err != nil {
		t.Errorf("open alert after resolve: %v", err)
	}
	if err := insertAlert("m3", "resolved"); err != nil {
		t.Errorf("second resolved alert: %v", err)
	}
}

func TestMembershipNotifyDefault(t *testing.T) {
	db := openTestDB(t, Options{})
	seedBasics(t, db)
	fixture(t, db, `INSERT INTO memberships (channel_id, user_id, joined_at) VALUES ('c1', 'u1', 1), ('d1', 'u1', 1)`)
	fixture(t, db, `INSERT INTO memberships (channel_id, user_id, joined_at, notify_level) VALUES ('d1', 'bot1', 1, 'none')`)

	tests := []struct{ channel, user, want string }{
		{"c1", "u1", "mentions"},
		{"d1", "u1", "all"},
		{"d1", "bot1", "none"},
	}
	for _, tc := range tests {
		var got string
		err := db.Read().QueryRow(`SELECT notify_level FROM memberships WHERE channel_id = ? AND user_id = ?`,
			tc.channel, tc.user).Scan(&got)
		if err != nil || got != tc.want {
			t.Errorf("notify_level(%s,%s) = %q (err %v), want %q", tc.channel, tc.user, got, err, tc.want)
		}
	}
}

func TestMembershipNotifyLevelNotNullable(t *testing.T) {
	db := openTestDB(t, Options{})
	seedBasics(t, db)
	fixture(t, db, `INSERT INTO memberships (channel_id, user_id, joined_at) VALUES ('c1', 'u1', 1)`)

	err := execErr(db, `UPDATE memberships SET notify_level = NULL WHERE channel_id = 'c1' AND user_id = 'u1'`)
	if err == nil || !strings.Contains(err.Error(), "notify_level must not be NULL") {
		t.Errorf("UPDATE to NULL err = %v, want notify_level rejection", err)
	}
	fixture(t, db, `UPDATE memberships SET notify_level = 'none' WHERE channel_id = 'c1' AND user_id = 'u1'`)
	fixture(t, db, `UPDATE memberships SET muted_until = 5 WHERE channel_id = 'c1' AND user_id = 'u1'`)

	var got string
	if err := db.Read().QueryRow(`SELECT notify_level FROM memberships WHERE channel_id = 'c1'`).Scan(&got); err != nil || got != "none" {
		t.Errorf("notify_level = %q (err %v), want none", got, err)
	}
}

func TestSchemaConstraints(t *testing.T) {
	db := openTestDB(t, Options{})
	seedBasics(t, db)
	insertMessage(t, db, "m1", "hello")

	tests := []struct {
		name  string
		want  string
		query string
	}{
		{"second instance row", "exactly one row", `INSERT INTO instance (id, created_at) VALUES ('x', 1)`},
		{"username uppercase", "CHECK", `INSERT INTO users (id, username, kind, role, created_at) VALUES ('u9', 'Bob', 'human', 'member', 1)`},
		{"username too short", "CHECK", `INSERT INTO users (id, username, kind, role, created_at) VALUES ('u9', 'b', 'human', 'member', 1)`},
		{"username bad char", "CHECK", `INSERT INTO users (id, username, kind, role, created_at) VALUES ('u9', 'bo b', 'human', 'member', 1)`},
		{"username duplicate", "UNIQUE", `INSERT INTO users (id, username, kind, role, created_at) VALUES ('u9', 'alice', 'human', 'member', 1)`},
		{"bad role", "CHECK", `INSERT INTO users (id, username, kind, role, created_at) VALUES ('u9', 'bob', 'human', 'root', 1)`},
		{"public channel without name", "CHECK", `INSERT INTO channels (id, kind, created_at) VALUES ('c9', 'public', 1)`},
		{"channel name uppercase", "CHECK", `INSERT INTO channels (id, kind, name, created_at) VALUES ('c9', 'public', 'General', 1)`},
		{"channel name duplicate", "UNIQUE", `INSERT INTO channels (id, kind, name, created_at) VALUES ('c9', 'private', 'general', 1)`},
		{"membership unknown channel", "FOREIGN KEY", `INSERT INTO memberships (channel_id, user_id, joined_at) VALUES ('nope', 'u1', 1)`},
		{"message unknown author", "FOREIGN KEY", `INSERT INTO messages (id, channel_id, author_id, created_at) VALUES ('m9', 'c1', 'nobody', 1)`},
		{"duplicate nonce", "UNIQUE", `INSERT INTO messages (id, channel_id, author_id, client_nonce, created_at) VALUES
			('m8', 'c1', 'u1', 'n1', 1), ('m9', 'c1', 'u1', 'n1', 1)`},
		{"body too long", "CHECK", `INSERT INTO messages (id, channel_id, author_id, body, created_at) VALUES ('m9', 'c1', 'u1', printf('%.8001c', 'x'), 1)`},
		{"emoji over 64 bytes", "CHECK", `INSERT INTO reactions (message_id, user_id, emoji, created_at) VALUES ('m1', 'u1', printf('%.65c', 'x'), 1)`},
		{"alert javascript link", "CHECK", `INSERT INTO alerts (message_id, app_id, dedupe_key, severity, title, link, first_seen_at, last_seen_at)
			VALUES ('m1', 'a1', 'k', 'info', 't', 'javascript:alert(1)', 1, 1)`},
		{"invalid json setting", "CHECK", `INSERT INTO settings (key, value) VALUES ('k', 'not json')`},
		{"strict integer column", "cannot store", `INSERT INTO events (type, payload, created_at) VALUES ('typing', '{}', 'yesterday')`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := execErr(db, tc.query)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want error containing %q", err, tc.want)
			}
		})
	}
}
