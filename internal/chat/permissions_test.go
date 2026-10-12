package chat

import (
	"errors"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// code renders a Can result as one character.
func code(err error) byte {
	switch {
	case err == nil:
		return '.'
	case errors.Is(err, ErrNotFound):
		return 'N'
	case errors.Is(err, ErrForbidden):
		return 'F'
	case errors.Is(err, ErrConflict):
		return 'C'
	}
	return '?'
}

var allScopes = []string{ScopeMessagesRead, ScopeMessagesWrite, ScopeChannelsRead, ScopeDMWrite}

func TestReadContentNeedsMessagesReadForApps(t *testing.T) {
	member := ChannelInfo{ID: "c", Kind: protocol.ChannelPublic, Member: true}
	outsider := ChannelInfo{ID: "c", Kind: protocol.ChannelPublic}
	app := func(scopes ...string) Actor {
		return Actor{ID: "u", Role: protocol.RoleMember, Kind: protocol.KindApp, Scopes: scopes}
	}
	for _, tc := range []struct {
		name  string
		actor Actor
		info  ChannelInfo
		want  error
	}{
		{"human member", Actor{ID: "u", Role: protocol.RoleMember}, member, nil},
		{"app with messages:read", app(ScopeMessagesRead), member, nil},
		{"app with channels:read only", app(ScopeChannelsRead), member, ErrForbidden},
		{"app with no scope", app(), member, ErrForbidden},
		{"app outside the channel", app(ScopeMessagesRead), outsider, ErrNotFound},
	} {
		err := Can(tc.actor, ActReadContent, tc.info)
		if (tc.want == nil) != (err == nil) || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// The columns of every expectation string follow Actions:
//
//	view post join leave manage add_member remove_member delete_any pin react archive create_channel start_dm
//
// '.' allowed, 'N' not found (invisible), 'F' forbidden, 'C' conflict.
// create_channel only applies to public and private kinds and start_dm only
// to dm and group_dm; the other kind is forbidden.
func TestPermissionMatrix(t *testing.T) {
	const (
		non    = "non-member"
		member = "member"
		owner  = "owner"
	)
	actors := []struct {
		name  string
		actor Actor
	}{
		{"admin", Actor{ID: "u", Role: protocol.RoleAdmin}},
		{"member", Actor{ID: "u", Role: protocol.RoleMember}},
		{"guest", Actor{ID: "u", Role: protocol.RoleGuest}},
		{"app", Actor{ID: "u", Role: protocol.RoleMember, Kind: protocol.KindApp, Scopes: allScopes}},
		{"app-noscope", Actor{ID: "u", Role: protocol.RoleMember, Kind: protocol.KindApp}},
	}
	// want[kind][actor][state]
	want := map[string]map[string]map[string]string{
		protocol.ChannelPublic: {
			"admin":       {non: ". F . F . . . . F F . . F", member: ". . . . . . . . . . . . F", owner: ". . . . . . . . . . . . F"},
			"member":      {non: ". F . F F F F F F F F . F", member: ". . . . F F F F . . F . F", owner: ". . . . . . . . . . . . F"},
			"guest":       {non: "N N N N N N N N N N N F F", member: ". . . . F F F F . . F F F", owner: ". . . . F F F F . . F F F"},
			"app":         {non: "N N N N N N N N N N N F F", member: ". . F F F F F F . . F F F", owner: ". . F F F F F F . . F F F"},
			"app-noscope": {non: "N N N N N N N N N N N F F", member: "F F F F F F F F F F F F F", owner: "F F F F F F F F F F F F F"},
		},
		protocol.ChannelPrivate: {
			"admin":       {non: "N N N N N N N N N N N . F", member: ". . . . . . . . . . . . F", owner: ". . . . . . . . . . . . F"},
			"member":      {non: "N N N N N N N N N N N . F", member: ". . . . F F F F . . F . F", owner: ". . . . . . . . . . . . F"},
			"guest":       {non: "N N N N N N N N N N N F F", member: ". . . . F F F F . . F F F", owner: ". . . . F F F F . . F F F"},
			"app":         {non: "N N N N N N N N N N N F F", member: ". . F F F F F F . . F F F", owner: ". . F F F F F F . . F F F"},
			"app-noscope": {non: "N N N N N N N N N N N F F", member: "F F F F F F F F F F F F F", owner: "F F F F F F F F F F F F F"},
		},
		protocol.ChannelDM: {
			"admin":       {non: "N N N N N N N N N N N F .", member: ". . F F F F F . . . F F .", owner: ". . F F F F F . . . F F ."},
			"member":      {non: "N N N N N N N N N N N F .", member: ". . F F F F F F . . F F .", owner: ". . F F F F F F . . F F ."},
			"guest":       {non: "N N N N N N N N N N N F .", member: ". . F F F F F F . . F F .", owner: ". . F F F F F F . . F F ."},
			"app":         {non: "N N N N N N N N N N N F .", member: ". . F F F F F F . . F F .", owner: ". . F F F F F F . . F F ."},
			"app-noscope": {non: "N N N N N N N N N N N F F", member: "F F F F F F F F F F F F F", owner: "F F F F F F F F F F F F F"},
		},
	}
	want[protocol.ChannelGroupDM] = want[protocol.ChannelDM]

	for kind, byActor := range want {
		for _, a := range actors {
			for state, exp := range byActor[a.name] {
				exp = strings.ReplaceAll(exp, " ", "")
				if len(exp) != len(Actions) {
					t.Fatalf("%s/%s/%s: expectation %q has %d columns, want %d", kind, a.name, state, exp, len(exp), len(Actions))
				}
				info := ChannelInfo{ID: "c", Kind: kind, Member: state != non, Owner: state == owner}
				var got strings.Builder
				for _, act := range Actions {
					got.WriteByte(code(Can(a.actor, act, info)))
				}
				if got.String() != exp {
					t.Errorf("%s / %s / %s:\n  got  %s\n  want %s\n  cols %s", kind, a.name, state, got.String(), exp, columns())
				}
			}
		}
	}
}

func columns() string {
	names := make([]string, len(Actions))
	for i, a := range Actions {
		names[i] = string(a)
	}
	return strings.Join(names, " ")
}

func TestPermissionArchivedChannelsAreReadOnly(t *testing.T) {
	// Everything that changes content or membership is a conflict; reading,
	// leaving and unarchiving still work.
	cases := []struct {
		name  string
		actor Actor
		info  ChannelInfo
		want  string // post join leave manage add remove delete_any pin react archive
	}{
		{"owner", Actor{ID: "u", Role: protocol.RoleMember}, ChannelInfo{ID: "c", Kind: protocol.ChannelPrivate, Archived: true, Member: true, Owner: true}, "C..CCCCCC."},
		{"admin on public", Actor{ID: "u", Role: protocol.RoleAdmin}, ChannelInfo{ID: "c", Kind: protocol.ChannelPublic, Archived: true}, "FCFCCCCFF."},
		{"plain member", Actor{ID: "u", Role: protocol.RoleMember}, ChannelInfo{ID: "c", Kind: protocol.ChannelPublic, Archived: true, Member: true}, "C..FFFFCCF"},
	}
	// Columns: post join leave manage add_member remove_member delete_any pin react archive
	acts := []Action{ActPost, ActJoin, ActLeave, ActManage, ActAddMember, ActRemoveMember, ActDeleteAny, ActPin, ActReact, ActArchive}
	for _, c := range cases {
		var got strings.Builder
		for _, act := range acts {
			got.WriteByte(code(Can(c.actor, act, c.info)))
		}
		if got.String() != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got.String(), c.want)
		}
	}
}

func TestPermissionAppAllowlist(t *testing.T) {
	app := Actor{ID: "bot", Kind: protocol.KindApp, Role: protocol.RoleMember, Scopes: allScopes, ChannelIDs: []string{"allowed"}}
	member := func(id string) ChannelInfo {
		return ChannelInfo{ID: id, Kind: protocol.ChannelPublic, Member: true}
	}
	if err := Can(app, ActPost, member("allowed")); err != nil {
		t.Errorf("allowed channel: %v", err)
	}
	if err := Can(app, ActPost, member("other")); !errors.Is(err, ErrNotFound) {
		t.Errorf("channel outside the allowlist: %v, want ErrNotFound", err)
	}
	app.ChannelIDs = nil
	if err := Can(app, ActPost, member("other")); err != nil {
		t.Errorf("no allowlist: %v", err)
	}
}

func TestPermissionUnknownRoleIsForbidden(t *testing.T) {
	if err := Can(Actor{ID: "u", Role: "root"}, ActView, ChannelInfo{Kind: protocol.ChannelPublic, Member: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unknown role: %v, want ErrForbidden", err)
	}
}
