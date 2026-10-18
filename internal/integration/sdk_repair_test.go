package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// pendingByCode returns the admin's view of the pending request with code.
func (s *server) pendingByCode(code string) (protocol.PendingPairing, bool) {
	s.t.Helper()
	var pending protocol.PendingPairingList
	s.admin("GET", "/api/v1/admin/pairing-requests", nil, &pending)
	for _, it := range pending.Items {
		if it.Code == code {
			return it, true
		}
	}
	return protocol.PendingPairing{}, false
}

// TestSDKReconnectDisconnectAndCancel is what an app's Reconnect, Disconnect
// and Cancel buttons do: a re-pairing that names the app reconnects it (no
// second app), the old token stops at the claim, the app can revoke its own
// token, and a request can be withdrawn while Wait runs.
func TestSDKReconnectDisconnectAndCancel(t *testing.T) {
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	scopes := []string{protocol.ScopeAlertsWrite}

	cfg1, first := s.pair("netdiag", scopes, true)
	c1 := s.client(cfg1)
	self, err := c1.Self(ctx)
	if err != nil || self.App.ID != first.App.ID {
		t.Fatalf("Self = %+v, %v", self, err)
	}

	// Reconnect: the new request names the app it was paired as.
	p, err := bubblesclient.RequestPairing(ctx, s.baseURL, s.fp, protocol.PairingRequest{
		AppName: "netdiag", RequestedScopes: scopes, ReplacesAppID: self.App.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, ok := s.pendingByCode(p.Code())
	if !ok || req.ID != p.ID() || req.Replaces == nil || req.Replaces.AppID != self.App.ID || req.Replaces.Username != "netdiag" {
		t.Fatalf("pending request = %+v (found %v)", req, ok)
	}
	var approval protocol.PairingApproval
	s.admin("POST", "/api/v1/admin/pairing-requests/"+req.ID+"/approve",
		protocol.ApprovePairingRequest{ReplaceApp: true, CreateChannel: true}, &approval)
	if approval.App.ID != self.App.ID {
		t.Fatalf("replace approval app = %s, want %s", approval.App.ID, self.App.ID)
	}
	// Until the app claims the new token, the old one keeps working.
	if _, err := c1.Self(ctx); err != nil {
		t.Fatalf("old token before the claim: %v", err)
	}
	cfg2, err := p.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if chans := p.Channels(); len(chans) != 1 || chans[0].Name != "netdiag-alerts" {
		t.Errorf("channels after the reconnect = %+v", chans)
	}
	c2 := s.client(cfg2)
	if self2, err := c2.Self(ctx); err != nil || self2.App.ID != self.App.ID || self2.App.Username != "netdiag" {
		t.Fatalf("Self with the new token = %+v, %v", self2, err)
	}
	if _, err := c1.Self(ctx); !errors.Is(err, bubblesclient.ErrUnauthorized) {
		t.Errorf("old token after the claim: %v, want ErrUnauthorized", err)
	}
	var apps protocol.AppList
	s.admin("GET", "/api/v1/admin/apps", nil, &apps)
	if len(apps.Items) != 1 || apps.Items[0].Username != "netdiag" {
		t.Errorf("apps after the reconnect = %+v", apps.Items)
	}

	// Disconnect: the app revokes its own token.
	if err := c2.RevokeSelf(ctx); err != nil {
		t.Fatalf("RevokeSelf: %v", err)
	}
	if _, err := c2.Self(ctx); !errors.Is(err, bubblesclient.ErrUnauthorized) {
		t.Errorf("Self after RevokeSelf: %v, want ErrUnauthorized", err)
	}

	// Cancel: a request withdrawn while Wait runs.
	p3, err := bubblesclient.RequestPairing(ctx, s.baseURL, s.fp, protocol.PairingRequest{
		AppName: "netdiag", RequestedScopes: scopes, ReplacesAppID: self.App.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := p3.Wait(ctx)
		done <- err
	}()
	if err := p3.Cancel(ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := <-done; !errors.Is(err, bubblesclient.ErrPairingCancelled) {
		t.Errorf("Wait after Cancel: %v, want ErrPairingCancelled", err)
	}
	if _, ok := s.pendingByCode(p3.Code()); ok {
		t.Error("the cancelled request is still pending for the admin")
	}
}
