package chat

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestBootstrap(t *testing.T) {
	e := newEnv(t)
	admin := e.human("root", protocol.RoleAdmin)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	guest := e.human("guest", protocol.RoleGuest)
	mate := e.human("mate", protocol.RoleMember)
	gone := e.human("gone", protocol.RoleMember)

	general := e.channel(admin, "general", protocol.ChannelPublic, alice, bob)
	e.channel(alice, "secret", protocol.ChannelPrivate, bob)
	shared := e.channel(alice, "client", protocol.ChannelPrivate, guest, mate)
	dm, _, err := e.svc.StartDM(e.ctx, bob.Actor, []string{alice.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.post(bob, general.ID, "hello @alice")
	e.post(bob, dm.ID, "psst")
	e.post(mate, shared.ID, "welcome")
	e.deactivate(gone)

	b, err := e.svc.Bootstrap(e.ctx, alice.Actor)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if b.Me.ID != alice.ID || b.Me.Role != protocol.RoleMember || b.Me.Username != "alice" || string(b.Me.Prefs) != "{}" {
		t.Fatalf("me = %+v", b.Me)
	}
	if b.SeqHead != e.head() || b.SeqHead == 0 {
		t.Fatalf("seq_head = %d, event head %d", b.SeqHead, e.head())
	}
	var instanceID string
	var epoch int64
	if err := e.db.Read().QueryRow(`SELECT id, epoch FROM instance`).Scan(&instanceID, &epoch); err != nil {
		t.Fatal(err)
	}
	if b.InstanceID != instanceID || b.Epoch != epoch || epoch != 1 {
		t.Fatalf("instance %q epoch %d, want %q epoch %d", b.InstanceID, b.Epoch, instanceID, epoch)
	}
	if b.SettingsPublic.InstanceName == "" || b.SettingsPublic.UploadMaxBytes <= 0 {
		t.Fatalf("settings_public = %+v", b.SettingsPublic)
	}

	// Non-guests know every account (deactivated ones flagged).
	if len(b.Users) != 6 {
		t.Fatalf("%d users, want 6", len(b.Users))
	}
	for _, u := range b.Users {
		if u.ID == gone.ID != u.Deactivated {
			t.Fatalf("user %s deactivated = %v", u.Username, u.Deactivated)
		}
	}
	names := channelNames(b.Channels)
	if !slices.Equal(names, []string{"client", "dm", "general", "secret"}) {
		t.Fatalf("channels = %v", names)
	}
	if len(b.Memberships) != 4 {
		t.Fatalf("%d memberships, want 4", len(b.Memberships))
	}
	counts := map[string]protocol.Unread{}
	for _, u := range b.Unread {
		counts[u.ChannelID] = u
	}
	if len(counts) != 4 || counts[general.ID].Unread != 1 || counts[general.ID].Mentions != 1 || counts[dm.ID].Unread != 1 || counts[dm.ID].Mentions != 0 {
		t.Fatalf("unread = %+v", counts)
	}

	// A member sees public channels they have not joined in the directory but not
	// other people's private ones.
	carol := e.human("carol", protocol.RoleMember)
	cb, err := e.svc.Bootstrap(e.ctx, carol.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if names := channelNames(cb.Channels); !slices.Equal(names, []string{"general"}) || len(cb.Memberships) != 0 || len(cb.Unread) != 0 {
		t.Fatalf("carol: channels %v memberships %d unread %d", names, len(cb.Memberships), len(cb.Unread))
	}

	// A guest sees only itself and the users it shares a channel with, and no directory.
	gb, err := e.svc.Bootstrap(e.ctx, guest.Actor)
	if err != nil {
		t.Fatal(err)
	}
	var who []string
	for _, u := range gb.Users {
		who = append(who, u.Username)
	}
	slices.Sort(who)
	if !slices.Equal(who, []string{"alice", "guest", "mate"}) {
		t.Fatalf("guest users = %v", who)
	}
	if names := channelNames(gb.Channels); !slices.Equal(names, []string{"client"}) {
		t.Fatalf("guest channels = %v", names)
	}
	// Once a member DMs the guest, that member becomes visible to it.
	if _, _, err := e.svc.StartDM(e.ctx, bob.Actor, []string{guest.ID}); err != nil {
		t.Fatal(err)
	}
	gb, _ = e.svc.Bootstrap(e.ctx, guest.Actor)
	if !slices.ContainsFunc(gb.Users, func(u protocol.User) bool { return u.ID == bob.ID }) {
		t.Fatal("a DM partner must be visible to a guest")
	}

	_, err = e.svc.Bootstrap(e.ctx, Actor{ID: "nobody"})
	wantErr(t, err, ErrNotFound)
}

func channelNames(chs []protocol.Channel) []string {
	var out []string
	for _, c := range chs {
		if c.Name != nil {
			out = append(out, *c.Name)
		} else {
			out = append(out, "dm")
		}
	}
	slices.Sort(out)
	return out
}

// TestWireShapes pins the JSON field names and nulls of the documented types.
func TestWireShapes(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic)
	msg := e.post(alice, ch.ID, "shape")

	keys := func(v any) map[string]json.RawMessage {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]json.RawMessage{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	sorted := func(m map[string]json.RawMessage) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		slices.Sort(ks)
		return ks
	}

	m := keys(msg)
	want := []string{"alert", "author", "body", "channel_id", "created_at", "deleted", "edited_at", "files", "id", "kind",
		"last_reply_at", "mentions", "override_name", "reactions", "reply_count", "thread_root_id"}
	if got := sorted(m); !slices.Equal(got, want) {
		t.Fatalf("message keys = %v\nwant %v", got, want)
	}
	for _, k := range []string{"alert", "edited_at", "last_reply_at", "override_name", "thread_root_id"} {
		if string(m[k]) != "null" {
			t.Errorf("%s = %s, want null", k, m[k])
		}
	}
	if string(m["files"]) != "[]" || string(m["reactions"]) != "[]" || string(m["mentions"]) != `{"user_ids":[],"channel":false,"here":false}` {
		t.Errorf("collections: files=%s reactions=%s mentions=%s", m["files"], m["reactions"], m["mentions"])
	}
	author := keys(msg.Author)
	if got := sorted(author); !slices.Equal(got, []string{"avatar_url", "display_name", "id", "kind", "username"}) || string(author["avatar_url"]) != "null" {
		t.Errorf("author = %s", author)
	}

	c := keys(ch)
	wantCh := []string{"archived_at", "created_at", "created_by", "description", "id", "is_default", "kind", "member_count", "name", "retention_days", "topic"}
	if got := sorted(c); !slices.Equal(got, wantCh) {
		t.Errorf("channel keys = %v\nwant %v", got, wantCh)
	}

	// File and alert shapes (populated through the fake attacher and a raw alert).
	f := keys(protocol.File{ID: "f", Name: "n", URL: "u"})
	wantFile := []string{"category", "has_thumb", "height", "id", "meta_stripped", "mime", "name", "size", "thumb_url", "url", "width"}
	if got := sorted(f); !slices.Equal(got, wantFile) {
		t.Errorf("file keys = %v\nwant %v", got, wantFile)
	}
	a := keys(protocol.Alert{})
	wantAlert := []string{"acked_at", "acked_by", "dedupe_key", "fields", "first_seen_at", "last_seen_at", "link", "occurrences",
		"resolved_at", "resolved_by", "severity", "state", "title"}
	if got := sorted(a); !slices.Equal(got, wantAlert) {
		t.Errorf("alert keys = %v\nwant %v", got, wantAlert)
	}
	if protocol.FileURL("id1", "a b.png") != "/api/v1/files/id1/a%20b.png" {
		t.Errorf("FileURL = %s", protocol.FileURL("id1", "a b.png"))
	}
}
