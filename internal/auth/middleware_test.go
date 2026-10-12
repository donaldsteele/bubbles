package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func probe(t *testing.T, mw func(http.Handler) http.Handler, mod func(*http.Request)) (user *User, authErr error, p *Principal) {
	t.Helper()
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		user, authErr, p = UserFrom(r.Context()), ErrorFrom(r.Context()), PrincipalFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if mod != nil {
		mod(req)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return user, authErr, p
}

func TestMiddlewareLoadsUserFromCookie(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleAdmin)
	res := e.mustLogin("alice")
	mw := Middleware(e.svc.CookieAuthenticator())

	user, err, p := probe(t, mw, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: res.Token}) })
	if err != nil || user == nil || user.Username != "alice" || user.Role != RoleAdmin {
		t.Fatalf("user = %+v, err = %v", user, err)
	}
	if p.Via != ViaCookie || p.Session == nil || p.Session.ID != res.Session.ID {
		t.Errorf("principal = %+v", p)
	}

	for name, mod := range map[string]func(*http.Request){
		"no cookie":       nil,
		"unknown token":   func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: "nope"}) },
		"empty token":     func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: ""}) },
		"other cookie":    func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "session", Value: res.Token}) },
		"unprefixed name": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "bubbles_session", Value: res.Token}) },
	} {
		if user, err, p := probe(t, mw, mod); user != nil || err != nil || p != nil {
			t.Errorf("%s: user=%+v err=%v", name, user, err)
		}
	}
}

func TestCookieAuthenticatorIgnoresBearerRequests(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	res := e.mustLogin("alice")
	mw := Middleware(e.svc.CookieAuthenticator())
	user, _, _ := probe(t, mw, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: res.Token})
		r.Header.Set("Authorization", "Bearer bub_whatever")
	})
	if user != nil {
		t.Error("a bearer request was authenticated by its cookie")
	}
}

type stubAuthenticator struct {
	p   *Principal
	err error
}

func (s stubAuthenticator) Authenticate(*http.Request) (*Principal, error) { return s.p, s.err }

func TestMiddlewareExtensionPoint(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	res := e.mustLogin("alice")
	bearer := &Principal{User: &User{ID: "app1", Username: "bot", Kind: KindApp}, Via: ViaBearer}

	// The first authenticator that recognizes the request wins.
	mw := Middleware(stubAuthenticator{p: bearer}, e.svc.CookieAuthenticator())
	user, _, p := probe(t, mw, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: res.Token}) })
	if user == nil || user.Username != "bot" || p.Via != ViaBearer || p.Session != nil {
		t.Errorf("principal = %+v", p)
	}

	// An infrastructure error is surfaced, not mistaken for "anonymous".
	boom := errors.New("db down")
	_, err, p := probe(t, Middleware(stubAuthenticator{err: boom}, e.svc.CookieAuthenticator()), nil)
	if !errors.Is(err, boom) || p != nil {
		t.Errorf("err = %v, principal = %+v", err, p)
	}
}

func TestRoles(t *testing.T) {
	for s, want := range map[string]bool{"admin": true, "member": true, "guest": true, "root": false, "": false, "Admin": false} {
		if _, err := ParseRole(s); (err == nil) != want {
			t.Errorf("ParseRole(%q) err = %v", s, err)
		}
	}
	if !RoleAdmin.AtLeast(RoleMember) || !RoleMember.AtLeast(RoleGuest) || RoleGuest.AtLeast(RoleMember) || Role("x").AtLeast(RoleGuest) {
		t.Error("AtLeast ordering")
	}
	if !RoleAdmin.IsAdmin() || RoleMember.IsAdmin() {
		t.Error("IsAdmin")
	}
}
