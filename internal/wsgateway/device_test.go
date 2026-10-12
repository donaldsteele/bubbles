package wsgateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func (e *env) deviceToken(t *testing.T, user, deviceID string) string {
	t.Helper()
	tok := user + "/device/" + deviceID
	e.auth.mu.Lock()
	defer e.auth.mu.Unlock()
	e.auth.m[tok] = &auth.Principal{
		User:   &auth.User{ID: user, Username: user, Kind: auth.KindHuman, Role: auth.RoleMember},
		Via:    auth.ViaDevice,
		Device: &auth.DeviceGrant{ID: deviceID, Name: "test", Scopes: protocol.DeviceScopes},
	}
	return tok
}

func TestDeviceSocketSeesUserEventsAndClosesOnRevoke(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	e.channel(t, "c1", protocol.ChannelPublic, "alice")
	e.exec(t, `INSERT INTO device_tokens (id, user_id, name, token_hash, prefix, scopes, created_at) VALUES ('d1', 'alice', 'n', x'01', 'bub_aaaa', '[]', 0)`)
	e.exec(t, `INSERT INTO device_tokens (id, user_id, name, token_hash, prefix, scopes, created_at) VALUES ('d2', 'alice', 'n', x'02', 'bub_bbbb', '[]', 0)`)
	d1 := e.dialBearer(t, e.deviceToken(t, "alice", "d1"))
	d2 := e.dialBearer(t, e.deviceToken(t, "alice", "d2"))
	web := e.dial(t, e.token("alice", auth.RoleMember, "sess-1"), nil)
	web.hello()

	seqs := e.append(t, msg("c1"))
	f := d1.mustNext()
	for f.Seq == 0 {
		f = d1.mustNext()
	}
	if f.Seq != seqs[0] {
		t.Fatalf("device saw seq %d, want the user message %d", f.Seq, seqs[0])
	}

	// A revoke event naming one device closes only that device socket.
	e.append(t, events.Event{Type: events.TypeSessionRevoked, UserID: "alice", Payload: json.RawMessage(`{"token_id":"d1"}`)})
	if code := d1.closeCode(); code != protocol.CloseSessionRevoked {
		t.Fatalf("revoked device socket closed with %d", code)
	}
	// Revoking every device (password change) closes the rest.
	if err := e.db.Do(context.Background(), func(tx *store.Tx) error { return auth.RevokeDevicesTx(tx, "alice") }); err != nil {
		t.Fatal(err)
	}
	if code := d2.closeCode(); code != protocol.CloseSessionRevoked {
		t.Fatalf("second device socket closed with %d", code)
	}
	m := marker(t, e)
	web.eventsUntil(m) // the browser session stays open
}

func TestViewerForDevice(t *testing.T) {
	v := viewerFor(&auth.Principal{
		User:   &auth.User{ID: "u", Kind: protocol.KindHuman, Role: auth.RoleMember},
		Via:    auth.ViaDevice,
		Device: &auth.DeviceGrant{ID: "d1", Scopes: protocol.DeviceScopes},
	})
	if v.TokenID != "d1" || v.AppID != "" || v.CommandsOnly || v.NoContent || v.ChannelIDs != nil {
		t.Fatalf("device viewer = %+v", v)
	}
}
