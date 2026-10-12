package wsgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/httpapi"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// TestRouteCookieSessionAndChatEvents runs the real stack: httpapi routing,
// cookie session authentication, the Origin check, and a chat mutation
// published through the post-commit hook.
func TestRouteCookieSessionAndChatEvents(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, store.FileName), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	st := settings.New(db)
	cs := chat.New(db, st)
	svc, err := auth.New(auth.Options{
		DB: db, Settings: st, DataDir: dir,
		Params:  auth.Params{Time: 1, MemoryKiB: 8, Threads: 1, SaltLen: 16, KeyLen: 32},
		OnSetup: cs.OnSetupTx, OnUserCreated: cs.OnUserCreatedTx,
	})
	if err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub(events.HubOptions{DB: db})
	gw := New(Options{Hub: hub, DB: db, Version: "v-int"})
	srv := httptest.NewServer(httpapi.New(httpapi.Deps{Version: "v-int", Auth: svc, Chat: cs, WS: gw}))
	t.Cleanup(func() {
		gw.CloseAll(int(websocket.StatusGoingAway), "")
		srv.Close()
		hub.Close()
		db.Close()
	})

	setupToken, err := svc.InitSetup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Setup(ctx, auth.SetupInput{
		Token: setupToken, Username: "admin", Password: "plum-lantern-quietly-42", DisplayName: "Admin", InstanceName: "Home", IP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + protocol.WSPath
	header := func(origin string) http.Header {
		h := http.Header{}
		h.Set("Cookie", auth.CookieName+"="+res.Token)
		if origin != "" {
			h.Set("Origin", origin)
		}
		return h
	}

	for _, origin := range []string{"", "http://evil.example"} {
		ws, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header(origin)})
		if err == nil {
			ws.CloseNow()
			t.Fatalf("origin %q: upgrade succeeded", origin)
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("origin %q: response %v, want 403", origin, resp)
		}
	}

	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dctx, wsURL, &websocket.DialOptions{HTTPHeader: header(srv.URL)})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer ws.CloseNow()
	c := &client{t: t, ws: ws}
	if h := c.hello(); h.UserID != res.User.ID || h.Resume != protocol.ResumeOK || h.ServerVersion != "v-int" {
		t.Fatalf("hello = %+v", h)
	}

	actor := chat.Actor{ID: res.User.ID, Role: string(res.User.Role), Kind: res.User.Kind}
	chans, err := cs.ListChannels(ctx, actor, chat.ScopeJoined)
	if err != nil || len(chans) == 0 {
		t.Fatalf("ListChannels = %v, %v", chans, err)
	}
	msg, _, err := cs.CreateMessage(ctx, actor, chat.CreateMessageParams{ChannelID: chans[0].ID, Body: "hello"})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	for {
		f := c.mustNext()
		if f.Type != events.TypeMessageCreated {
			continue
		}
		var got protocol.Message
		if err := json.Unmarshal(f.Data, &got); err != nil || got.ID != msg.ID || f.ChannelID != chans[0].ID || f.Seq == 0 {
			t.Fatalf("message frame %s", f.raw)
		}
		break
	}

	// Logging out revokes the session: its socket closes with 4009.
	if err := svc.Logout(ctx, res.Token, "127.0.0.1"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if code := c.closeCode(); code != protocol.CloseSessionRevoked {
		t.Fatalf("close code %d, want 4009", code)
	}
}
