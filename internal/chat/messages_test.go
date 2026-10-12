package chat

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestCreateMessageBasics(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic)

	before := e.head()
	m, created, err := e.svc.CreateMessage(e.ctx, alice.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "\n  \n  hello **world**  \n\n"})
	if err != nil || !created {
		t.Fatalf("CreateMessage: %v created=%v", err, created)
	}
	if m.Body != "  hello **world**" || m.Kind != protocol.MessageText || m.Author.ID != alice.ID || m.Author.Username != "alice" ||
		m.ThreadRootID != nil || m.Deleted || m.ReplyCount != 0 || m.Alert != nil {
		t.Fatalf("message = %+v", m)
	}
	if m.Files == nil || m.Reactions == nil || m.Mentions.UserIDs == nil {
		t.Fatal("empty collections must be [] on the wire, not null")
	}
	evs := e.events(before)
	wantTypes(t, evs, "message.created")
	if evs[0].Channel != ch.ID || evs[0].User != "" || !strings.Contains(evs[0].Payload, `"id":"`+m.ID+`"`) {
		t.Fatalf("event = %+v", evs[0])
	}
	if got := e.count(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'hello'`); got != 1 {
		t.Fatalf("FTS rows for body_plain = %d", got)
	}
	var plain string
	if err := e.db.Read().QueryRow(`SELECT body_plain FROM messages WHERE id = ?`, m.ID).Scan(&plain); err != nil || plain != "hello world" {
		t.Fatalf("body_plain = %q, %v", plain, err)
	}
}

func TestMessageLimits(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic)
	post := func(p CreateMessageParams) error {
		p.ChannelID = ch.ID
		_, _, err := e.svc.CreateMessage(e.ctx, alice.Actor, p)
		return err
	}

	for name, body := range map[string]string{"empty": "", "blank": " \n\t ", "too long": strings.Repeat("x", MaxBodyChars+1), "NUL": "a\x00b", "bad utf8": "a\xffb"} {
		wantField(t, post(CreateMessageParams{Body: body}), "body")
		_ = name
	}
	if err := post(CreateMessageParams{Body: strings.Repeat("x", MaxBodyChars)}); err != nil {
		t.Fatalf("8000 chars: %v", err)
	}
	// The limit counts characters, not bytes.
	if err := post(CreateMessageParams{Body: strings.Repeat("é", MaxBodyChars)}); err != nil {
		t.Fatalf("8000 multi-byte chars: %v", err)
	}
	wantField(t, post(CreateMessageParams{Body: strings.Repeat("é", MaxBodyChars+1)}), "body")

	wantField(t, post(CreateMessageParams{Body: "x", Nonce: strings.Repeat("n", MaxNonceLen+1)}), "nonce")
	wantField(t, post(CreateMessageParams{Body: "x", Kind: protocol.MessageSystem}), "kind")
	wantField(t, post(CreateMessageParams{Body: "x", OverrideName: "Hook"}), "override_name")
	wantField(t, post(CreateMessageParams{Body: "x", ThreadRootID: "nope"}), "thread_root_id")

	// Files: no attacher means no attachments.
	wantField(t, post(CreateMessageParams{Body: "x", FileIDs: []string{"f1"}}), "file_ids")
	att := &fakeAttacher{}
	e.svc.SetFileAttacher(att)
	wantField(t, post(CreateMessageParams{Body: "x", FileIDs: make([]string, MaxFileIDs+1)}), "file_ids")
	wantField(t, post(CreateMessageParams{Body: "x", FileIDs: []string{"f1", "f1"}}), "file_ids")
	if att.calls != 0 {
		t.Fatalf("attacher called %d times for invalid requests", att.calls)
	}
}

func TestMessageWithFiles(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic)
	att := &fakeAttacher{}
	e.svc.SetFileAttacher(att)

	before := e.head()
	m, _, err := e.svc.CreateMessage(e.ctx, alice.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "", FileIDs: []string{"f1", "f2"}})
	if err != nil {
		t.Fatalf("files without text: %v", err)
	}
	if len(m.Files) != 2 || m.Files[0].ID != "f1" || m.Files[1].ID != "f2" || att.calls != 1 {
		t.Fatalf("files = %+v, calls %d", m.Files, att.calls)
	}
	evs := e.events(before)
	wantTypes(t, evs, "message.created")
	if !strings.Contains(evs[0].Payload, `"f2"`) {
		t.Fatalf("event payload lacks the files: %s", evs[0].Payload)
	}
	got, err := e.svc.GetMessage(e.ctx, alice.Actor, m.ID)
	if err != nil || len(got.Files) != 2 || got.Files[0].URL != protocol.FileURL("f1", got.Files[0].Name) {
		t.Fatalf("stored files = %+v, %v", got.Files, err)
	}

	// An attacher failure aborts the whole message.
	att.err = errors.New("quota")
	head := e.head()
	_, _, err = e.svc.CreateMessage(e.ctx, alice.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "x", FileIDs: []string{"f3"}})
	if err == nil {
		t.Fatal("attacher error swallowed")
	}
	if e.head() != head || e.count(`SELECT COUNT(*) FROM messages`) != 1 {
		t.Fatal("a failed attach left a message or an event behind")
	}
}

func TestNonceIdempotency(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic, bob)

	p := CreateMessageParams{ChannelID: ch.ID, Body: "once", Nonce: "n-1"}
	first, created, err := e.svc.CreateMessage(e.ctx, alice.Actor, p)
	if err != nil || !created {
		t.Fatalf("first: %v created=%v", err, created)
	}
	head := e.head()
	p.Body = "a retry may even differ"
	again, created, err := e.svc.CreateMessage(e.ctx, alice.Actor, p)
	if err != nil || created || again.ID != first.ID || again.Body != "once" {
		t.Fatalf("retry: %+v created=%v err=%v", again, created, err)
	}
	if e.head() != head {
		t.Fatal("a retry emitted an event")
	}
	if n := e.count(`SELECT COUNT(*) FROM messages`); n != 1 {
		t.Fatalf("%d messages, want 1", n)
	}

	// Nonces are per author.
	other, created, err := e.svc.CreateMessage(e.ctx, bob.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "mine", Nonce: "n-1"})
	if err != nil || !created || other.ID == first.ID {
		t.Fatalf("other author: %+v created=%v err=%v", other, created, err)
	}

	// A retry after the channel was archived still gets the stored message.
	if _, err := e.svc.ArchiveChannel(e.ctx, alice.Actor, ch.ID); err != nil {
		t.Fatal(err)
	}
	head = e.head()
	again, created, err = e.svc.CreateMessage(e.ctx, alice.Actor, p)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("retry after archive: %+v created=%v err=%v", again, created, err)
	}
	if e.head() != head {
		t.Fatal("retry after archive emitted an event")
	}

	// The trusted path is idempotent too.
	var viaTx protocol.Message
	err = e.db.Do(e.ctx, func(tx *store.Tx) error {
		var err error
		viaTx, err = e.svc.CreateMessageTx(tx, CreateMessageParams{ChannelID: ch.ID, AuthorID: alice.ID, Body: "x", Nonce: "n-1"})
		return err
	})
	if err != nil || viaTx.ID != first.ID {
		t.Fatalf("CreateMessageTx with a known nonce: %+v, %v", viaTx, err)
	}
}

func TestMessagePermissions(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	outsider := e.human("outsider", protocol.RoleMember)
	guest := e.human("guest", protocol.RoleGuest)
	pub := e.channel(alice, "pub", protocol.ChannelPublic)
	priv := e.channel(alice, "priv", protocol.ChannelPrivate)

	body := CreateMessageParams{Body: "hi"}
	body.ChannelID = priv.ID
	_, _, err := e.svc.CreateMessage(e.ctx, outsider.Actor, body)
	wantErr(t, err, ErrNotFound)
	body.ChannelID = pub.ID
	_, _, err = e.svc.CreateMessage(e.ctx, outsider.Actor, body)
	wantErr(t, err, ErrForbidden)
	_, _, err = e.svc.CreateMessage(e.ctx, guest.Actor, body)
	wantErr(t, err, ErrNotFound)
	body.ChannelID = "missing"
	_, _, err = e.svc.CreateMessage(e.ctx, alice.Actor, body)
	wantErr(t, err, ErrNotFound)

	// The author comes from the actor; a spoofed author id is ignored.
	body.ChannelID = pub.ID
	body.AuthorID = outsider.ID
	m, _, err := e.svc.CreateMessage(e.ctx, alice.Actor, body)
	if err != nil || m.Author.ID != alice.ID {
		t.Fatalf("spoofed author: %+v, %v", m.Author, err)
	}
}

func TestMentions(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	carol := e.human("carol", protocol.RoleMember)
	dave := e.human("dave", protocol.RoleMember) // not in the channel
	erin := e.human("erin", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPrivate, bob, carol, erin)
	e.deactivate(erin)

	m := e.post(alice, ch.ID, "@bob @Carol @dave @erin @alice @nobody and `@carol` ok")
	if want := []string{bob.ID, carol.ID}; !slices.Equal(m.Mentions.UserIDs, sortedCopy(want)) {
		t.Fatalf("mentions = %v, want %v (members only, no self, no deactivated)", m.Mentions.UserIDs, sortedCopy(want))
	}
	if m.Mentions.Channel || m.Mentions.Here {
		t.Fatalf("flags = %+v", m.Mentions)
	}

	// Mentions inside code are ignored, outside are not.
	m = e.post(alice, ch.ID, "```\n@bob @channel\n```\nplain @here")
	if len(m.Mentions.UserIDs) != 0 || m.Mentions.Channel || !m.Mentions.Here {
		t.Fatalf("code mentions: %+v", m.Mentions)
	}
	m = e.post(alice, ch.ID, "run `@bob` and    @carol")
	if !slices.Equal(m.Mentions.UserIDs, []string{carol.ID}) {
		t.Fatalf("inline code mention: %+v", m.Mentions)
	}
	_ = dave

	// An edit re-extracts: the old mention goes, the new one arrives.
	edited, err := e.svc.EditMessage(e.ctx, alice.Actor, m.ID, "@bob @channel only")
	if err != nil || !slices.Equal(edited.Mentions.UserIDs, []string{bob.ID}) || !edited.Mentions.Channel {
		t.Fatalf("edit: %+v, %v", edited.Mentions, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM mentions WHERE message_id = ?`, m.ID); n != 1 {
		t.Fatalf("%d mention rows after edit, want 1", n)
	}
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}

func TestEditMessage(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic, bob)
	msg := e.post(alice, ch.ID, "first")

	before := e.head()
	got, err := e.svc.EditMessage(e.ctx, alice.Actor, msg.ID, "  second  ")
	if err != nil || got.Body != "  second" || got.EditedAt == nil {
		t.Fatalf("EditMessage: %+v, %v", got, err)
	}
	wantTypes(t, e.events(before), "message.updated")
	if n := e.count(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'first'`); n != 0 {
		t.Fatal("FTS still indexes the old body")
	}
	if n := e.count(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'second'`); n != 1 {
		t.Fatal("FTS does not index the new body")
	}

	// Same body: nothing happens.
	before = e.head()
	if _, err := e.svc.EditMessage(e.ctx, alice.Actor, msg.ID, "  second"); err != nil {
		t.Fatal(err)
	}
	wantTypes(t, e.events(before))

	_, err = e.svc.EditMessage(e.ctx, bob.Actor, msg.ID, "hijack")
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.EditMessage(e.ctx, alice.Actor, msg.ID, " ")
	wantField(t, err, "body")
	_, err = e.svc.EditMessage(e.ctx, alice.Actor, "missing", "x")
	wantErr(t, err, ErrNotFound)

	outsider := e.human("outsider", protocol.RoleMember)
	priv := e.channel(alice, "priv", protocol.ChannelPrivate)
	hidden := e.post(alice, priv.ID, "secret")
	_, err = e.svc.EditMessage(e.ctx, outsider.Actor, hidden.ID, "x")
	wantErr(t, err, ErrNotFound)

	// Deleted messages cannot be edited.
	if err := e.svc.DeleteMessage(e.ctx, alice.Actor, msg.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.EditMessage(e.ctx, alice.Actor, msg.ID, "back from the dead")
	wantErr(t, err, ErrConflict)

	// A message with files may be edited down to nothing.
	e.svc.SetFileAttacher(&fakeAttacher{})
	withFile, _, err := e.svc.CreateMessage(e.ctx, alice.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "caption", FileIDs: []string{"f9"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := e.svc.EditMessage(e.ctx, alice.Actor, withFile.ID, ""); err != nil || got.Body != "" || len(got.Files) != 1 {
		t.Fatalf("clearing the caption: %+v, %v", got, err)
	}
}

func TestDeleteMessageTombstone(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	e.svc.SetFileAttacher(&fakeAttacher{})
	ch := e.channel(alice, "talk", protocol.ChannelPublic, bob)

	msg, _, err := e.svc.CreateMessage(e.ctx, bob.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "secret plan @alice", FileIDs: []string{"f1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AddReaction(e.ctx, alice.Actor, msg.ID, "👍"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PinMessage(e.ctx, alice.Actor, ch.ID, msg.ID); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM reactions`, `SELECT COUNT(*) FROM mentions`, `SELECT COUNT(*) FROM pins`, `SELECT COUNT(*) FROM attachments`,
	} {
		if e.count(q) != 1 {
			t.Fatalf("setup: %s != 1", q)
		}
	}
	if e.count(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'secret'`) != 1 {
		t.Fatal("setup: message not indexed")
	}

	before := e.head()
	if err := e.svc.DeleteMessage(e.ctx, bob.Actor, msg.ID); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	evs := e.events(before)
	wantTypes(t, evs, "pin.removed", "message.deleted")
	if evs[1].Channel != ch.ID || !strings.Contains(evs[1].Payload, msg.ID) {
		t.Fatalf("message.deleted = %+v", evs[1])
	}

	got, err := e.svc.GetMessage(e.ctx, alice.Actor, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Deleted || got.Body != "" || len(got.Files) != 0 || len(got.Reactions) != 0 || len(got.Mentions.UserIDs) != 0 || got.Author.ID != bob.ID {
		t.Fatalf("tombstone = %+v", got)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM reactions`, `SELECT COUNT(*) FROM mentions`, `SELECT COUNT(*) FROM pins`, `SELECT COUNT(*) FROM attachments`,
		`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'secret'`,
	} {
		if n := e.count(q); n != 0 {
			t.Errorf("%s = %d after delete, want 0", q, n)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE id = ? AND body = '' AND body_plain = '' AND deleted_at IS NOT NULL`, msg.ID); n != 1 {
		t.Fatal("the tombstone row is not blanked")
	}

	// Deleting again is a quiet no-op.
	before = e.head()
	if err := e.svc.DeleteMessage(e.ctx, bob.Actor, msg.ID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	wantTypes(t, e.events(before))
}

func TestWhoMayDelete(t *testing.T) {
	e := newEnv(t)
	owner := e.human("owner", protocol.RoleMember)
	author := e.human("author", protocol.RoleMember)
	peer := e.human("peer", protocol.RoleMember)
	admin := e.human("root", protocol.RoleAdmin)
	guest := e.human("guest", protocol.RoleGuest)
	pub := e.channel(owner, "pub", protocol.ChannelPublic, author, peer, guest)
	priv := e.channel(owner, "priv", protocol.ChannelPrivate, author, peer)
	dm, _, err := e.svc.StartDM(e.ctx, author.Actor, []string{peer.ID})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		ch     string
		by     user
		target error
	}{
		{"author", pub.ID, author, nil},
		{"channel owner", pub.ID, owner, nil},
		{"admin outside a public channel", pub.ID, admin, nil},
		{"plain member", pub.ID, peer, ErrForbidden},
		{"guest member", pub.ID, guest, ErrForbidden},
		{"admin outside a private channel", priv.ID, admin, ErrNotFound},
		{"owner of a private channel", priv.ID, owner, nil},
		{"peer in a private channel", priv.ID, peer, ErrForbidden},
		{"dm peer", dm.ID, peer, ErrForbidden},
		{"dm author", dm.ID, author, nil},
	}
	for _, c := range cases {
		msg := e.post(author, c.ch, "to be deleted: "+c.name)
		err := e.svc.DeleteMessage(e.ctx, c.by.Actor, msg.ID)
		if c.target == nil && err != nil || c.target != nil && !errors.Is(err, c.target) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.target)
		}
		got, _ := e.svc.GetMessage(e.ctx, author.Actor, msg.ID)
		if wantDeleted := c.target == nil; got.Deleted != wantDeleted {
			t.Errorf("%s: deleted = %v, want %v", c.name, got.Deleted, wantDeleted)
		}
	}
}

func TestThreads(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic, bob)
	other := e.channel(alice, "other", protocol.ChannelPublic)
	root := e.post(alice, ch.ID, "question?")
	foreign := e.post(alice, other.ID, "elsewhere")

	before := e.head()
	r1, created, err := e.svc.CreateMessage(e.ctx, bob.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "answer", ThreadRootID: root.ID})
	if err != nil || !created {
		t.Fatalf("reply: %v", err)
	}
	if r1.ThreadRootID == nil || *r1.ThreadRootID != root.ID {
		t.Fatalf("reply root = %v", r1.ThreadRootID)
	}
	evs := e.events(before)
	wantTypes(t, evs, "message.created", "message.updated")
	if !strings.Contains(evs[1].Payload, `"reply_count":1`) || !strings.Contains(evs[1].Payload, `"id":"`+root.ID+`"`) {
		t.Fatalf("root update = %s", evs[1].Payload)
	}

	// A reply to a reply lands in the root's thread.
	r2 := e.reply(alice, ch.ID, r1.ID, "follow-up")
	if r2.ThreadRootID == nil || *r2.ThreadRootID != root.ID {
		t.Fatalf("reply-to-reply root = %v, want %s", r2.ThreadRootID, root.ID)
	}
	got, err := e.svc.GetMessage(e.ctx, alice.Actor, root.ID)
	if err != nil || got.ReplyCount != 2 || got.LastReplyAt == nil || *got.LastReplyAt != r2.CreatedAt {
		t.Fatalf("root = %+v, %v", got, err)
	}

	// The root must be in the same channel.
	_, _, err = e.svc.CreateMessage(e.ctx, alice.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "x", ThreadRootID: foreign.ID})
	wantField(t, err, "thread_root_id")

	// Channel listing shows roots only; the thread lists the replies.
	page, err := e.svc.ListMessages(e.ctx, alice.Actor, ch.ID, PageOptions{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != root.ID || page.Items[0].ReplyCount != 2 {
		t.Fatalf("channel page = %+v, %v", page, err)
	}
	th, err := e.svc.ListThread(e.ctx, bob.Actor, r2.ID, PageOptions{})
	if err != nil || th.Root.ID != root.ID || len(th.Replies) != 2 || th.Replies[0].ID != r1.ID || th.Replies[1].ID != r2.ID {
		t.Fatalf("thread = %+v, %v", th, err)
	}

	// Deleting a reply shrinks the thread and updates the root.
	before = e.head()
	if err := e.svc.DeleteMessage(e.ctx, alice.Actor, r2.ID); err != nil {
		t.Fatal(err)
	}
	wantTypes(t, e.events(before), "message.deleted", "message.updated")
	got, _ = e.svc.GetMessage(e.ctx, alice.Actor, root.ID)
	if got.ReplyCount != 1 || got.LastReplyAt == nil || *got.LastReplyAt != r1.CreatedAt {
		t.Fatalf("root after reply delete = %+v", got)
	}

	// A deleted root with replies stays as a tombstone; the replies survive.
	if err := e.svc.DeleteMessage(e.ctx, alice.Actor, root.ID); err != nil {
		t.Fatal(err)
	}
	th, err = e.svc.ListThread(e.ctx, bob.Actor, root.ID, PageOptions{})
	if err != nil || !th.Root.Deleted || len(th.Replies) != 2 {
		t.Fatalf("thread of a deleted root = %+v, %v", th, err)
	}
	if th.Root.ReplyCount != 1 {
		t.Fatalf("tombstone reply_count = %d, want 1", th.Root.ReplyCount)
	}
	// Replying to the tombstoned root still works (the conversation continues).
	if _, _, err := e.svc.CreateMessage(e.ctx, bob.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "late", ThreadRootID: root.ID}); err != nil {
		t.Fatalf("reply to a tombstone: %v", err)
	}
}

func TestPagination(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic)
	var ids []string
	for i := 0; i < 10; i++ {
		ids = append(ids, e.post(alice, ch.ID, "m"+string(rune('a'+i))).ID)
	}
	list := func(o PageOptions) ([]string, protocol.MessagePage) {
		p, err := e.svc.ListMessages(e.ctx, alice.Actor, ch.ID, o)
		if err != nil {
			t.Fatalf("ListMessages(%+v): %v", o, err)
		}
		got := make([]string, len(p.Items))
		for i, m := range p.Items {
			got[i] = m.ID
		}
		return got, p
	}
	check := func(name string, got []string, p protocol.MessagePage, want []string, before, after bool) {
		t.Helper()
		if !slices.Equal(got, want) || p.HasMoreBefore != before || p.HasMoreAfter != after {
			t.Errorf("%s: ids %v more(before=%v after=%v), want %v more(before=%v after=%v)",
				name, idxs(ids, got), p.HasMoreBefore, p.HasMoreAfter, idxs(ids, want), before, after)
		}
	}
	got, p := list(PageOptions{Limit: 4})
	check("latest", got, p, ids[6:], true, false)
	got, p = list(PageOptions{Limit: 4, Before: ids[6]})
	check("before", got, p, ids[2:6], true, true)
	got, p = list(PageOptions{Limit: 4, Before: ids[2]})
	check("before, last page", got, p, ids[0:2], false, true)
	got, p = list(PageOptions{Limit: 4, After: ids[3]})
	check("after", got, p, ids[4:8], true, true)
	got, p = list(PageOptions{Limit: 4, After: ids[6]})
	check("after, last page", got, p, ids[7:], true, false)
	got, p = list(PageOptions{Limit: 5, Around: ids[5]})
	check("around", got, p, ids[3:8], true, true)
	got, p = list(PageOptions{Limit: 20, Around: ids[5]})
	check("around, all", got, p, ids, false, false)
	got, p = list(PageOptions{})
	check("default limit", got, p, ids, false, false)

	_, err := e.svc.ListMessages(e.ctx, alice.Actor, ch.ID, PageOptions{Before: ids[1], After: ids[2]})
	wantField(t, err, "before")
	_, err = e.svc.ListMessages(e.ctx, alice.Actor, ch.ID, PageOptions{Around: "missing"})
	wantErr(t, err, ErrNotFound)
}

func idxs(all, sub []string) []int {
	out := []int{}
	for _, s := range sub {
		out = append(out, slices.Index(all, s))
	}
	return out
}

func TestOverrideNameAndAfterInsertViaTx(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	hook := e.addUser("hook", protocol.RoleMember, protocol.KindApp)
	ch := e.channel(alice, "alerts", protocol.ChannelPublic)

	var seen string
	before := e.head()
	var msg protocol.Message
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		var err error
		msg, err = e.svc.CreateMessageTx(tx, CreateMessageParams{
			ChannelID: ch.ID, AuthorID: hook.ID, Kind: protocol.MessageAlert, Body: "disk full", OverrideName: "backup-job",
			AfterInsert: func(tx *store.Tx, id string) error {
				seen = id
				_, err := tx.Exec(`INSERT INTO apps (id, user_id, name, owner_user_id, created_at) VALUES (?, ?, 'hook', ?, ?)`, "app1", hook.ID, alice.ID, tx.Now())
				if err != nil {
					return err
				}
				_, err = tx.Exec(`INSERT INTO alerts (message_id, app_id, dedupe_key, severity, title, first_seen_at, last_seen_at)
					VALUES (?, 'app1', 'k', 'critical', 'Disk', ?, ?)`, id, tx.Now(), tx.Now())
				return err
			},
		})
		return err
	})
	if err != nil {
		t.Fatalf("CreateMessageTx: %v", err)
	}
	if msg.OverrideName == nil || *msg.OverrideName != "backup-job" || msg.Kind != protocol.MessageAlert || msg.Author.Kind != protocol.KindApp {
		t.Fatalf("message = %+v", msg)
	}
	if seen != msg.ID || msg.Alert == nil || msg.Alert.Severity != protocol.SeverityCritical || msg.Alert.State != protocol.AlertOpen || msg.Alert.Title != "Disk" {
		t.Fatalf("alert not hydrated into the created message: seen=%q alert=%+v", seen, msg.Alert)
	}
	evs := e.events(before)
	wantTypes(t, evs, "message.created")
	if !strings.Contains(evs[0].Payload, `"title":"Disk"`) {
		t.Fatalf("event payload lacks the alert: %s", evs[0].Payload)
	}

	// The author need not be a member on the trusted path, but the channel must exist and be open.
	err = e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := e.svc.CreateMessageTx(tx, CreateMessageParams{ChannelID: "missing", AuthorID: hook.ID, Body: "x"})
		return err
	})
	wantErr(t, err, ErrNotFound)
	err = e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := e.svc.CreateMessageTx(tx, CreateMessageParams{ChannelID: ch.ID, AuthorID: "ghost", Body: "x"})
		return err
	})
	wantField(t, err, "author_id")
}
