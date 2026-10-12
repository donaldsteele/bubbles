package httpapi

import (
	"net/http"
	"testing"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// TestAppChannelsReadOnlyGetsNoMessageBodies: a channels:read token sees the
// channel, but its message list is 403; an app outside the channel gets 404.
func TestAppChannelsReadOnlyGetsNoMessageBodies(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	ch := admin.createChannel(t, "ops", protocol.ChannelPublic)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/channels/"+ch.ID+"/messages", protocol.CreateMessageRequest{Body: "secret"}), http.StatusCreated, "")

	dir := e.newBot(admin, "dir", []string{"channels:read"}, nil, ch.ID)
	wantStatus(t, dir.do(e, http.MethodGet, "/api/v1/channels", nil), http.StatusOK, "")
	wantStatus(t, dir.do(e, http.MethodGet, "/api/v1/channels/"+ch.ID+"/messages", nil), http.StatusForbidden, CodeForbidden)

	reader := e.newBot(admin, "reader", []string{"messages:read"}, nil, ch.ID)
	wantStatus(t, reader.do(e, http.MethodGet, "/api/v1/channels/"+ch.ID+"/messages", nil), http.StatusOK, "")

	outside := e.newBot(admin, "outside", []string{"channels:read"}, nil)
	wantStatus(t, outside.do(e, http.MethodGet, "/api/v1/channels/"+ch.ID+"/messages", nil), http.StatusNotFound, CodeNotFound)
}

// TestPostAlertMentionsNothing: the direct POST /alerts path stores the alert
// text without mention rows or flags, whatever the text says.
func TestPostAlertMentionsNothing(t *testing.T) {
	e := newEnv(t)
	f := e.alertFixture()
	rec := f.post(e, alertIn(func(in *protocol.AlertInput) { in.Text = "@channel @here @alice" }))
	wantStatus(t, rec, http.StatusCreated, "")
	msg := decodeBody[protocol.PostedAlert](t, rec).Message
	if n := e.count(`SELECT COUNT(*) FROM mentions WHERE message_id = ?`, msg.ID); n != 0 {
		t.Errorf("mention rows = %d, want 0", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE id = ? AND (mention_channel = 1 OR mention_here = 1)`, msg.ID); n != 0 {
		t.Errorf("mention flags set on alert")
	}
}
