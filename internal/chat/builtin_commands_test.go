package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestCommandBuiltins(t *testing.T) {
	e := newEnv(t)
	owner, bob, carol := e.human("alice", protocol.RoleMember), e.human("bob", protocol.RoleMember), e.human("carol", protocol.RoleMember)
	ch := e.channel(owner, "ops", protocol.ChannelPublic, bob)
	run := func(u user, cmd, args string) string {
		t.Helper()
		res, err := e.svc.RunBuiltin(e.ctx, u.Actor, BuiltinInput{Command: cmd, Args: args, ChannelID: ch.ID})
		if err != nil {
			t.Fatalf("/%s: %v", cmd, err)
		}
		return res.Notice
	}

	// /topic needs the manage permission.
	if n := run(owner, "topic", "all hands"); n != "" {
		t.Fatalf("/topic notice %q", n)
	}
	if got, _ := e.svc.GetChannel(e.ctx, owner.Actor, ch.ID); got.Topic != "all hands" {
		t.Fatalf("topic = %q", got.Topic)
	}
	if n := run(bob, "topic", "nope"); !strings.Contains(n, "channel owner") {
		t.Fatalf("member /topic notice = %q", n)
	}
	if n := run(owner, "topic", ""); !strings.HasPrefix(n, "Usage") {
		t.Fatalf("empty /topic notice = %q", n)
	}
	if n := run(owner, "topic", strings.Repeat("x", 251)); !strings.Contains(n, "topic") {
		t.Fatalf("long /topic notice = %q", n)
	}

	// /invite
	if n := run(owner, "invite", "@carol"); n != "" {
		t.Fatalf("/invite notice %q", n)
	}
	if _, err := e.svc.GetChannel(e.ctx, carol.Actor, ch.ID); err != nil {
		t.Fatalf("carol not added: %v", err)
	}
	if n := run(owner, "invite", "@ghost"); !strings.Contains(n, "No such user") {
		t.Fatalf("unknown user notice = %q", n)
	}
	if n := run(owner, "invite", ""); !strings.HasPrefix(n, "Usage") {
		t.Fatalf("empty /invite notice = %q", n)
	}

	// /me posts an emphasised normal message.
	if n := run(bob, "me", "waves"); n != "" {
		t.Fatalf("/me notice %q", n)
	}
	page, err := e.svc.ListMessages(e.ctx, bob.Actor, ch.ID, PageOptions{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Body != "_Bob waves_" {
		t.Fatalf("/me message = %+v, %v", page, err)
	}

	// /mute
	for _, tc := range []struct {
		args string
		want time.Duration
	}{{"30m", 30 * time.Minute}, {"2h", 2 * time.Hour}, {"1d", 24 * time.Hour}, {"", time.Hour}} {
		if n := run(bob, "mute", tc.args); n != "" {
			t.Fatalf("/mute %q notice %q", tc.args, n)
		}
		var ms int64
		if err := e.db.Read().QueryRow(`SELECT muted_until FROM memberships WHERE channel_id = ? AND user_id = ?`, ch.ID, bob.ID).Scan(&ms); err != nil {
			t.Fatal(err)
		}
		if got := time.UnixMilli(ms).Sub(e.clock.Now()); got < tc.want-time.Second || got > tc.want+time.Second {
			t.Fatalf("/mute %q muted for %v, want %v", tc.args, got, tc.want)
		}
	}
	run(bob, "mute", "until-tomorrow")
	var ms int64
	if err := e.db.Read().QueryRow(`SELECT muted_until FROM memberships WHERE channel_id = ? AND user_id = ?`, ch.ID, bob.ID).Scan(&ms); err != nil {
		t.Fatal(err)
	}
	if u := time.UnixMilli(ms).In(time.Local); u.Hour() != 0 || u.Minute() != 0 || !u.After(e.clock.Now()) || u.Sub(e.clock.Now()) > 25*time.Hour {
		t.Fatalf("until-tomorrow = %v", u)
	}
	for _, bad := range []string{"soon", "0m", "-5m", "99999d", "10"} {
		if n := run(bob, "mute", bad); !strings.HasPrefix(n, "Usage") {
			t.Fatalf("/mute %q notice = %q", bad, n)
		}
	}
	run(bob, "mute", "off")
	var muted *int64
	if err := e.db.Read().QueryRow(`SELECT muted_until FROM memberships WHERE channel_id = ? AND user_id = ?`, ch.ID, bob.ID).Scan(&muted); err != nil || muted != nil {
		t.Fatalf("/mute off left %v (%v)", muted, err)
	}

	// /help lists the catalog.
	n := run(bob, "help", "")
	for _, c := range BuiltinCommands() {
		if !strings.Contains(n, c.Usage) {
			t.Fatalf("/help misses %s: %q", c.Name, n)
		}
	}

	// /leave
	if n := run(bob, "leave", ""); n != "" {
		t.Fatalf("/leave notice %q", n)
	}
	if got := e.count(`SELECT COUNT(*) FROM memberships WHERE channel_id = ? AND user_id = ?`, ch.ID, bob.ID); got != 0 {
		t.Fatal("bob still a member")
	}
	// Misuse by a non-member is a notice, not an error.
	if n := run(bob, "me", "hi"); n == "" {
		t.Fatal("non-member /me gave no notice")
	}
}

func TestCommandCheckPost(t *testing.T) {
	e := newEnv(t)
	owner, outsider := e.human("alice", protocol.RoleMember), e.human("bob", protocol.RoleMember)
	priv := e.channel(owner, "secret", protocol.ChannelPrivate)
	if err := e.svc.CheckPost(e.ctx, owner.Actor, priv.ID, ""); err != nil {
		t.Fatalf("member: %v", err)
	}
	if err := e.svc.CheckPost(e.ctx, outsider.Actor, priv.ID, ""); err == nil {
		t.Fatal("outsider may post in a private channel")
	}
	if err := e.svc.CheckPost(e.ctx, owner.Actor, priv.ID, "01JZZZZZZZZZZZZZZZZZZZZZZZ"); err == nil {
		t.Fatal("unknown thread root accepted")
	}
}
