package alerts

import (
	"testing"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// noMentionRows fails the test if message id stores any mention row or flag.
func (e *env) noMentionRows(id string) {
	e.t.Helper()
	if n := e.count(`SELECT COUNT(*) FROM mentions WHERE message_id = ?`, id); n != 0 {
		e.t.Errorf("message %s: %d mention rows, want 0", id, n)
	}
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE id = ? AND (mention_channel = 1 OR mention_here = 1)`, id); n != 0 {
		e.t.Errorf("message %s: mention flags set", id)
	}
}

// TestAlertTextNeverMentions: an app's alert text cannot notify anyone, on the
// direct POST /alerts path, on a repeat that re-renders the body, or in the
// resolve reply.
func TestAlertTextNeverMentions(t *testing.T) {
	e := newEnv(t)
	const text = "@channel @here @alice"

	first := e.post(e.bot, warn(func(in *protocol.AlertInput) { in.Text = text }))
	if m := first.Message.Mentions; m.Channel || m.Here || len(m.UserIDs) != 0 {
		t.Errorf("alert mentions = %+v, want none", m)
	}
	e.noMentionRows(first.Message.ID)

	again := e.post(e.bot, warn(func(in *protocol.AlertInput) { in.Text = "@channel @alice again" }))
	if again.Message.ID != first.Message.ID || again.Transition != protocol.AlertTransitionUpdatedOther {
		t.Fatalf("repeat = %s on %s", again.Transition, again.Message.ID)
	}
	e.noMentionRows(first.Message.ID)

	if _, err := e.svc.ResolveByKey(e.ctx, e.bot, e.aud(e.bot), "disk", "@channel @here @alice fixed"); err != nil {
		t.Fatal(err)
	}
	var reply string
	if err := e.db.Read().QueryRow(`SELECT id FROM messages WHERE kind = 'system' ORDER BY id DESC LIMIT 1`).Scan(&reply); err != nil {
		t.Fatal(err)
	}
	e.noMentionRows(reply)
	e.noMentionRows(first.Message.ID)
}
