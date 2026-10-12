package chat

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// snapshot renders every table a chat mutation can touch, so a test can tell
// whether anything changed at all.
func (e *env) snapshot() string {
	e.t.Helper()
	var b strings.Builder
	for _, table := range []string{"channels", "memberships", "messages", "mentions", "reactions", "pins", "attachments", "files", "events"} {
		rows, err := e.db.Read().QueryContext(e.ctx, `SELECT * FROM `+table)
		if err != nil {
			e.t.Fatalf("snapshot %s: %v", table, err)
		}
		cols, _ := rows.Columns()
		fmt.Fprintf(&b, "[%s]\n", table)
		var lines []string
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				e.t.Fatalf("snapshot scan: %v", err)
			}
			lines = append(lines, fmt.Sprint(vals...))
		}
		rows.Close()
		slices.Sort(lines)
		b.WriteString(strings.Join(lines, "\n"))
		b.WriteString("\n")
	}
	var seq sql.NullInt64
	_ = e.db.Read().QueryRowContext(e.ctx, `SELECT seq FROM sqlite_sequence WHERE name = 'events'`).Scan(&seq)
	fmt.Fprintf(&b, "seq=%d", seq.Int64)
	return b.String()
}

var errBoom = errors.New("boom")

// TestEveryMutationEmitsItsEventsInTheSameTransaction runs each mutation
// through its Tx variant, checks the events it appended are visible inside the
// transaction and are exactly the expected types, then forces a rollback and
// checks that neither the rows nor the events survive.
func TestEveryMutationEmitsItsEventsInTheSameTransaction(t *testing.T) {
	probe := mutationCases(newMutationFixture(t))
	for i := range probe {
		t.Run(probe[i].name, func(t *testing.T) {
			f := newMutationFixture(t)
			runMutationCase(t, f, mutationCases(f)[i])
		})
	}
}

type mutationFixture struct {
	e                              *env
	admin, alice, bob, carol, late user
	pub, priv, arch                protocol.Channel
	m, pinned                      protocol.Message
}

type mutationCase struct {
	name string
	want []string
	fn   func(tx *store.Tx) error
}

func newMutationFixture(t *testing.T) *mutationFixture {
	e := newEnv(t)
	e.svc.SetFileAttacher(&fakeAttacher{})
	admin := e.human("root", protocol.RoleAdmin)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	carol := e.human("carol", protocol.RoleMember)
	late := e.human("late", protocol.RoleMember)

	pub := e.channel(alice, "pub", protocol.ChannelPublic, bob)
	priv := e.channel(alice, "priv", protocol.ChannelPrivate, bob)
	lobby := e.channel(admin, "lobby", protocol.ChannelPublic)
	if _, err := e.svc.SetDefault(e.ctx, admin.Actor, lobby.ID, true); err != nil {
		t.Fatal(err)
	}
	arch := e.channel(alice, "arch", protocol.ChannelPublic)
	if _, err := e.svc.ArchiveChannel(e.ctx, alice.Actor, arch.ID); err != nil {
		t.Fatal(err)
	}
	m := e.post(alice, pub.ID, "plain @bob")
	pinned := e.post(alice, pub.ID, "pinned")
	if _, err := e.svc.PinMessage(e.ctx, alice.Actor, pub.ID, pinned.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AddReaction(e.ctx, bob.Actor, m.ID, "👍"); err != nil {
		t.Fatal(err)
	}
	return &mutationFixture{e: e, admin: admin, alice: alice, bob: bob, carol: carol, late: late, pub: pub, priv: priv, arch: arch, m: m, pinned: pinned}
}

func mutationCases(f *mutationFixture) []mutationCase {
	e, admin, alice, bob, carol, late := f.e, f.admin, f.alice, f.bob, f.carol, f.late
	pub, priv, arch, m, pinned := f.pub, f.priv, f.arch, f.m, f.pinned
	return []mutationCase{
		{"CreateChannel", []string{"channel.created"}, func(tx *store.Tx) error {
			_, err := e.svc.CreateChannelTx(tx, alice.Actor, CreateChannelParams{Name: "fresh", Kind: protocol.ChannelPublic, MemberIDs: []string{bob.ID}})
			return err
		}},
		{"UpdateChannel", []string{"channel.updated"}, func(tx *store.Tx) error {
			_, err := e.svc.UpdateChannelTx(tx, alice.Actor, pub.ID, UpdateChannelParams{Topic: ptr("t")})
			return err
		}},
		{"ArchiveChannel", []string{"channel.archived"}, func(tx *store.Tx) error {
			_, err := e.svc.ArchiveChannelTx(tx, alice.Actor, pub.ID)
			return err
		}},
		{"UnarchiveChannel", []string{"channel.unarchived"}, func(tx *store.Tx) error {
			_, err := e.svc.UnarchiveChannelTx(tx, alice.Actor, arch.ID)
			return err
		}},
		{"SetDefault", []string{"channel.updated"}, func(tx *store.Tx) error {
			_, err := e.svc.SetDefaultTx(tx, admin.Actor, pub.ID, true)
			return err
		}},
		{"JoinChannel", []string{"channel.member_joined"}, func(tx *store.Tx) error {
			_, err := e.svc.JoinChannelTx(tx, carol.Actor, pub.ID)
			return err
		}},
		{"LeaveChannel", []string{"channel.member_left"}, func(tx *store.Tx) error {
			return e.svc.LeaveChannelTx(tx, bob.Actor, pub.ID)
		}},
		{"AddMember", []string{"channel.member_joined"}, func(tx *store.Tx) error {
			_, err := e.svc.AddMemberTx(tx, alice.Actor, priv.ID, carol.ID, "")
			return err
		}},
		{"AddMember promotes", []string{"membership.updated"}, func(tx *store.Tx) error {
			_, err := e.svc.AddMemberTx(tx, alice.Actor, priv.ID, bob.ID, protocol.MemberOwner)
			return err
		}},
		{"RemoveMember", []string{"channel.member_left"}, func(tx *store.Tx) error {
			return e.svc.RemoveMemberTx(tx, alice.Actor, pub.ID, bob.ID)
		}},
		{"UpdateMembership", []string{"membership.updated"}, func(tx *store.Tx) error {
			_, err := e.svc.UpdateMembershipTx(tx, bob.Actor, pub.ID, MembershipUpdate{NotifyLevel: ptr(protocol.NotifyNone)})
			return err
		}},
		{"StartDM", []string{"channel.created"}, func(tx *store.Tx) error {
			_, _, err := e.svc.StartDMTx(tx, alice.Actor, []string{carol.ID})
			return err
		}},
		{"CreateMessageAs", []string{"message.created"}, func(tx *store.Tx) error {
			_, _, err := e.svc.CreateMessageAsTx(tx, alice.Actor, CreateMessageParams{ChannelID: pub.ID, Body: "hi @bob", FileIDs: []string{"fx"}})
			return err
		}},
		{"CreateMessageAs reply", []string{"message.created", "message.updated"}, func(tx *store.Tx) error {
			_, _, err := e.svc.CreateMessageAsTx(tx, bob.Actor, CreateMessageParams{ChannelID: pub.ID, Body: "re", ThreadRootID: m.ID})
			return err
		}},
		{"CreateMessage trusted", []string{"message.created"}, func(tx *store.Tx) error {
			_, err := e.svc.CreateMessageTx(tx, CreateMessageParams{ChannelID: pub.ID, AuthorID: alice.ID, Kind: protocol.MessageSystem, Body: "notice"})
			return err
		}},
		{"EditMessage", []string{"message.updated"}, func(tx *store.Tx) error {
			_, err := e.svc.EditMessageTx(tx, alice.Actor, m.ID, "changed")
			return err
		}},
		{"DeleteMessage", []string{"message.deleted"}, func(tx *store.Tx) error {
			return e.svc.DeleteMessageTx(tx, alice.Actor, m.ID)
		}},
		{"DeleteMessage pinned", []string{"pin.removed", "message.deleted"}, func(tx *store.Tx) error {
			return e.svc.DeleteMessageTx(tx, alice.Actor, pinned.ID)
		}},
		{"AddReaction", []string{"reaction.added"}, func(tx *store.Tx) error {
			_, err := e.svc.AddReactionTx(tx, alice.Actor, m.ID, "🎉")
			return err
		}},
		{"RemoveReaction", []string{"reaction.removed"}, func(tx *store.Tx) error {
			_, err := e.svc.RemoveReactionTx(tx, bob.Actor, m.ID, "👍")
			return err
		}},
		{"PinMessage", []string{"pin.added"}, func(tx *store.Tx) error {
			_, err := e.svc.PinMessageTx(tx, bob.Actor, pub.ID, m.ID)
			return err
		}},
		{"UnpinMessage", []string{"pin.removed"}, func(tx *store.Tx) error {
			return e.svc.UnpinMessageTx(tx, bob.Actor, pub.ID, pinned.ID)
		}},
		{"MarkRead", []string{"read.updated"}, func(tx *store.Tx) error {
			_, err := e.svc.MarkReadTx(tx, bob.Actor, pub.ID, pinned.ID)
			return err
		}},
		{"OnBootstrap", []string{"channel.created"}, func(tx *store.Tx) error {
			return e.svc.OnBootstrapTx(tx.Context(), tx, admin.ID)
		}},
		{"OnUserCreated", []string{"channel.member_joined"}, func(tx *store.Tx) error {
			return e.svc.OnUserCreatedTx(tx.Context(), tx, late.ID)
		}},
	}
}

func runMutationCase(t *testing.T, f *mutationFixture, c mutationCase) {
	e := f.e
	before, head := e.snapshot(), e.head()
	var inTx []string
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		if err := c.fn(tx); err != nil {
			return fmt.Errorf("mutation failed: %w", err)
		}
		rows, err := tx.Query(`SELECT type FROM events WHERE seq > ? ORDER BY seq`, head)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var typ string
			if err := rows.Scan(&typ); err != nil {
				return err
			}
			inTx = append(inTx, typ)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Do = %v, want the forced rollback", err)
	}
	if !slices.Equal(inTx, c.want) {
		t.Fatalf("events inside the transaction = %v, want %v", inTx, c.want)
	}
	if e.head() != head || e.snapshot() != before {
		t.Fatalf("rollback left rows or events behind")
	}

	// Committed, the same call leaves exactly those events behind.
	if err := e.db.Do(e.ctx, c.fn); err != nil {
		t.Fatalf("committed run: %v", err)
	}
	wantTypes(t, e.events(head), c.want...)
}

// TestFailedMutationsLeaveNothing checks that a rejected request writes no
// rows and no events.
func TestFailedMutationsLeaveNothing(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic)
	msg := e.post(alice, ch.ID, "hello")
	before, head := e.snapshot(), e.head()

	bad := []func() error{
		func() error {
			_, _, err := e.svc.CreateMessage(e.ctx, bob.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "not a member"})
			return err
		},
		func() error {
			_, _, err := e.svc.CreateMessage(e.ctx, alice.Actor, CreateMessageParams{ChannelID: ch.ID, Body: ""})
			return err
		},
		func() error { _, err := e.svc.EditMessage(e.ctx, bob.Actor, msg.ID, "x"); return err },
		func() error { return e.svc.DeleteMessage(e.ctx, bob.Actor, msg.ID) },
		func() error { _, err := e.svc.AddReaction(e.ctx, alice.Actor, msg.ID, ""); return err },
		func() error { _, err := e.svc.PinMessage(e.ctx, bob.Actor, ch.ID, msg.ID); return err },
		func() error { _, err := e.svc.MarkRead(e.ctx, bob.Actor, ch.ID, msg.ID); return err },
		func() error {
			_, err := e.svc.CreateChannel(e.ctx, alice.Actor, CreateChannelParams{Name: "talk", Kind: protocol.ChannelPublic})
			return err
		},
		func() error { _, err := e.svc.AddMember(e.ctx, bob.Actor, ch.ID, bob.ID, ""); return err },
	}
	for i, fn := range bad {
		if err := fn(); err == nil {
			t.Errorf("case %d: expected an error", i)
		}
	}
	if e.snapshot() != before || e.head() != head {
		t.Fatal("rejected requests changed the database")
	}
}
