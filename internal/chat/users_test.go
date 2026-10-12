package chat

import (
	"errors"
	"testing"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func userNames(us []protocol.User) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = u.Username
	}
	return out
}

func TestListUsersVisibility(t *testing.T) {
	e := newEnv(t)
	admin := e.human("alice", protocol.RoleAdmin)
	member := e.human("bob", protocol.RoleMember)
	guest := e.human("gus", protocol.RoleGuest)
	friend := e.human("fay", protocol.RoleMember)
	stranger := e.human("sam", protocol.RoleMember)
	e.channel(admin, "shared", protocol.ChannelPrivate, guest, friend)

	cases := []struct {
		name  string
		actor user
		q     string
		want  []string
	}{
		{"member sees everyone", member, "", []string{"alice", "bob", "gus", "fay", "sam"}},
		{"admin query", admin, "A", []string{"alice", "fay", "sam"}},
		{"query matches display name", member, "Sam", []string{"sam"}},
		{"guest sees self and channel peers", guest, "", []string{"alice", "gus", "fay"}},
		{"guest query never reaches strangers", guest, "sam", []string{}},
		{"like wildcards are literal", member, "%", []string{}},
		{"underscore is literal", member, "_", []string{}},
	}
	for _, tc := range cases {
		got, err := e.svc.ListUsers(e.ctx, tc.actor.Actor, tc.q)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if g, w := userNames(got), tc.want; len(g) != len(w) {
			t.Errorf("%s: users = %v, want %v", tc.name, g, w)
			continue
		} else {
			for i := range w {
				if g[i] != w[i] {
					t.Errorf("%s: users = %v, want %v", tc.name, g, w)
					break
				}
			}
		}
	}

	if _, err := e.svc.GetUser(e.ctx, guest.Actor, stranger.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("guest GetUser(stranger) = %v, want not found", err)
	}
	if u, err := e.svc.GetUser(e.ctx, guest.Actor, friend.ID); err != nil || u.Username != "fay" {
		t.Errorf("guest GetUser(friend) = %+v, %v", u, err)
	}
	if _, err := e.svc.GetUser(e.ctx, member.Actor, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetUser(unknown) = %v, want not found", err)
	}
	long := make([]byte, MaxUserQueryChars+1)
	for i := range long {
		long[i] = 'a'
	}
	var ve *ValidationError
	if _, err := e.svc.ListUsers(e.ctx, member.Actor, string(long)); !errors.As(err, &ve) || ve.Fields["q"] == "" {
		t.Errorf("long q = %v, want a validation error on q", err)
	}
}

func TestListUsersQueryEscapesLikeMetacharacters(t *testing.T) {
	e := newEnv(t)
	viewer := e.human("viewer", protocol.RoleMember)
	// Usernames are restricted to [a-z0-9._-], so the metacharacters that the
	// query must match literally live in display names.
	setDisplayName := func(u user, name string) {
		t.Helper()
		if err := e.db.Do(e.ctx, func(tx *store.Tx) error {
			_, err := tx.Exec(`UPDATE users SET display_name = ? WHERE id = ?`, name, u.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	setDisplayName(e.human("pct", protocol.RoleMember), "100%")
	setDisplayName(e.human("und", protocol.RoleMember), "a_c")
	setDisplayName(e.human("bsl", protocol.RoleMember), `a\c`)
	e.human("nina", protocol.RoleMember)

	cases := []struct {
		name string
		q    string
		want []string
	}{
		{"percent is literal", "%", []string{"pct"}},
		{"underscore is literal", "_", []string{"und"}},
		{"backslash is literal", `\`, []string{"bsl"}},
		{"escaped backslash does not escape n", `\n`, []string{}},
		{"backslash pair matches literally", `a\c`, []string{"bsl"}},
		{"plain letters still match", "nin", []string{"nina"}},
	}
	for _, tc := range cases {
		got, err := e.svc.ListUsers(e.ctx, viewer.Actor, tc.q)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		g, w := userNames(got), tc.want
		if len(g) != len(w) {
			t.Errorf("%s: users = %v, want %v", tc.name, g, w)
			continue
		}
		for i := range w {
			if g[i] != w[i] {
				t.Errorf("%s: users = %v, want %v", tc.name, g, w)
				break
			}
		}
	}
}
