package httpapi

import (
	"net/http"
	"testing"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestRegisterAndLoginByName(t *testing.T) {
	e := newEnv(t)
	e.setup()
	if err := e.st.Set(e.ctx, audit.System, settings.KeyOpenRegistration, true); err != nil {
		t.Fatal(err)
	}
	c := e.client("")
	rec := c.do(http.MethodPost, "/api/v1/register", protocol.RegisterRequest{Name: "  Ada   Lovelace ", Password: goodPassword})
	wantStatus(t, rec, http.StatusCreated, "")
	if u := decodeBody[protocol.AuthResponse](t, rec).User; u.DisplayName != "Ada Lovelace" || u.Username != "ada-lovelace" {
		t.Errorf("user = %+v", u)
	}

	dup := wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/register", protocol.RegisterRequest{Name: "ADA lovelace", Password: goodPassword}), http.StatusConflict, CodeConflict)
	if dup.FieldErrors()["name"] == "" {
		t.Errorf("fields = %v, want name", dup.FieldErrors())
	}

	for _, name := range []string{"Ada Lovelace", "ada lovelace", "ada-lovelace"} {
		wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: name, Password: goodPassword}), http.StatusOK, "")
	}
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "Ada Lovelace", Password: "wrong password!"}), http.StatusUnauthorized, CodeUnauthorized)
}

func TestPatchMeNameTaken(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	bob := e.addUser("bob", "192.168.1.31")
	rec := bob.do(http.MethodPatch, "/api/v1/me", map[string]any{"display_name": "ADMIN"})
	ed := wantStatus(t, rec, http.StatusConflict, CodeConflict)
	if ed.FieldErrors()["display_name"] == "" {
		t.Errorf("fields = %v, want display_name", ed.FieldErrors())
	}
	wantStatus(t, admin.do(http.MethodPatch, "/api/v1/me", map[string]any{"display_name": "ADMIN"}), http.StatusOK, "")
}

func TestAcceptInviteByName(t *testing.T) {
	e := newEnv(t)
	e.setup()
	_, code, err := e.svc.CreateInvite(e.ctx, audit.Console, authInviteInput())
	if err != nil {
		t.Fatal(err)
	}
	rec := e.client("").do(http.MethodPost, "/api/v1/invites/"+code+"/accept", protocol.AcceptInviteRequest{Name: "José Núñez", Password: goodPassword})
	wantStatus(t, rec, http.StatusCreated, "")
	if u := decodeBody[protocol.AuthResponse](t, rec).User; u.DisplayName != "José Núñez" || u.Username != "jose-nunez" {
		t.Errorf("user = %+v", u)
	}
}

func authInviteInput() auth.CreateInviteInput { return auth.CreateInviteInput{} }
