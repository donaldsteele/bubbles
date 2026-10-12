package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
)

func TestHandleBase(t *testing.T) {
	for in, want := range map[string]string{
		"Ada Lovelace":                   "ada-lovelace",
		"Zoë":                            "zoe",
		"José Núñez":                     "jose-nunez",
		"李小龙":                            "user",
		"🎮 Gamer 🎮":                      "gamer",
		"x":                              "user",
		"  ..Bob.. ":                     "bob",
		"a_b.c-d":                        "a_b.c-d",
		"ａｄａ":                            "ada",
		strings.Repeat("abcdefghij ", 4): "abcdefghij-abcdefghij-abcdefghij",
	} {
		got := handleBase(in)
		if got != want {
			t.Errorf("handleBase(%q) = %q, want %q", in, got, want)
		}
		if !usernameRE.MatchString(got) {
			t.Errorf("handleBase(%q) = %q is not a valid username", in, got)
		}
	}
	if got := handleBase(strings.Repeat("a", 40)); got != strings.Repeat("a", 32) {
		t.Errorf("40-char name: %q", got)
	}
}

func TestNormalizeDisplayName(t *testing.T) {
	if got, why := normalizeDisplayName("  Ada \t  Lovelace  "); got != "Ada Lovelace" || why != "" {
		t.Errorf("got %q, %q", got, why)
	}
	if _, why := normalizeDisplayName(strings.Repeat("é", 65)); why == "" {
		t.Error("65 runes accepted")
	}
	if _, why := normalizeDisplayName("a\x00b"); why == "" {
		t.Error("control character accepted")
	}
	if got, why := normalizeDisplayName(" \n "); got != "" || why != "" {
		t.Errorf("blank: %q, %q", got, why)
	}
}

func TestDisplayKeyFolds(t *testing.T) {
	if displayKey("Ada") != displayKey("ADA") || displayKey("Ada") != displayKey("ａｄａ") {
		t.Errorf("keys differ: %q %q %q", displayKey("Ada"), displayKey("ADA"), displayKey("ａｄａ"))
	}
	if displayKey("Ada") == displayKey("Adam") {
		t.Error("distinct names share a key")
	}
}

func TestUniqueHandle(t *testing.T) {
	e := newEnv(t)
	for i, want := range []string{"ada", "ada-2", "ada-3"} {
		var got string
		err := e.db.Do(e.ctx, func(tx *store.Tx) error {
			h, err := uniqueHandle(tx, "ada")
			if err != nil {
				return err
			}
			got = h
			_, err = insertUser(tx, newUserFields{username: h, displayName: h, displayKey: h}, RoleMember, "x")
			return err
		})
		if err != nil || got != want {
			t.Fatalf("round %d: %q, %v; want %q", i, got, err, want)
		}
	}
	// A long base is shortened so the suffix still fits in 32 bytes.
	long := strings.Repeat("b", 32)
	e.addUser(long, RoleMember)
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		h, err := uniqueHandle(tx, long)
		if err != nil || h != strings.Repeat("b", 30)+"-2" {
			t.Errorf("long handle = %q, %v", h, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) openRegistration() {
	e.t.Helper()
	if err := e.settings.Set(e.ctx, audit.System, settings.KeyOpenRegistration, true); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) registerName(name string) (*SessionResult, error) {
	return e.svc.Register(e.ctx, RegisterInput{Name: name, Password: goodPassword, IP: "192.168.1.5"})
}

func TestRegisterByName(t *testing.T) {
	e := newEnv(t)
	e.openRegistration()
	res, err := e.registerName("  Ada   Lovelace ")
	if err != nil {
		t.Fatal(err)
	}
	if res.User.DisplayName != "Ada Lovelace" || res.User.Username != "ada-lovelace" {
		t.Fatalf("user = %+v", res.User)
	}
	if _, err := e.registerName("ADA lovelace"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := e.registerName("ａｄａ　ｌｏｖｅｌａｃｅ"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("fullwidth duplicate: %v", err)
	}
	// A different name that derives the same handle gets a numbered one.
	res2, err := e.registerName("Ada Lovelace!")
	if err != nil || res2.User.Username != "ada-lovelace-2" || res2.User.DisplayName != "Ada Lovelace!" {
		t.Fatalf("second: %+v, %v", res2, err)
	}
	var ve *ValidationError
	if _, err := e.registerName(strings.Repeat("n", 65)); !errors.As(err, &ve) || ve.Fields["name"] == "" {
		t.Errorf("long name: %v", err)
	}
}

func TestAcceptInviteByName(t *testing.T) {
	e := newEnv(t)
	admin := e.addUser("admin", RoleAdmin)
	_, code, err := e.svc.CreateInvite(e.ctx, audit.User(admin.ID, "192.168.1.2"), CreateInviteInput{MaxUses: 3})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.AcceptInvite(e.ctx, AcceptInviteInput{Code: code, Name: "Zoë Quinn", Password: goodPassword})
	if err != nil || res.User.Username != "zoe-quinn" || res.User.DisplayName != "Zoë Quinn" {
		t.Fatalf("accept: %+v, %v", res, err)
	}
	_, err = e.svc.AcceptInvite(e.ctx, AcceptInviteInput{Code: code, Name: "zoë QUINN", Password: goodPassword})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	if n := e.count(`SELECT use_count FROM invites`); n != 1 {
		t.Errorf("use_count = %d, want 1 (a failed accept consumes no use)", n)
	}
}

func TestUpdateProfileNameUniqueness(t *testing.T) {
	e := newEnv(t)
	e.openRegistration()
	e.addUser("alice", RoleMember)
	bob := e.addUser("bob", RoleMember)
	taken := "ALICE"
	if _, err := e.svc.UpdateProfile(e.ctx, bob.ID, ProfileInput{DisplayName: &taken}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("taken: %v", err)
	}
	own := "BOB"
	u, err := e.svc.UpdateProfile(e.ctx, bob.ID, ProfileInput{DisplayName: &own})
	if err != nil || u.DisplayName != "BOB" {
		t.Fatalf("own name, new case: %+v, %v", u, err)
	}
	fresh := "Robert  Tables"
	u, err = e.svc.UpdateProfile(e.ctx, bob.ID, ProfileInput{DisplayName: &fresh})
	if err != nil || u.DisplayName != "Robert Tables" {
		t.Fatalf("rename: %+v, %v", u, err)
	}
	if _, err := e.registerName("robert TABLES"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("key not updated by rename: %v", err)
	}
	if _, err := e.registerName("bob"); err != nil {
		t.Errorf("old name not released: %v", err)
	}
}

func TestLoginByDisplayName(t *testing.T) {
	e := newEnv(t)
	e.openRegistration()
	if _, err := e.registerName("Ada Lovelace"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Ada Lovelace", "  ada   LOVELACE ", "ada-lovelace", "ADA-Lovelace", "ａｄａ ｌｏｖｅｌａｃｅ"} {
		if _, err := e.login(name, goodPassword); err != nil {
			t.Errorf("login %q: %v", name, err)
		}
	}
	if _, err := e.login("Ada Lovelace", "wrong password!"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	// Login accepts a name of 64 multibyte characters without cutting it.
	long := strings.Repeat("é", 64)
	if _, err := e.registerName(long); err != nil {
		t.Fatal(err)
	}
	if _, err := e.login(long, goodPassword); err != nil {
		t.Errorf("64-rune name: %v", err)
	}
}

func TestLoginAmbiguousDisplayName(t *testing.T) {
	e := newEnv(t)
	e.addUser("sam-one", RoleMember)
	e.addUser("sam-two", RoleMember)
	e.execSQL(`UPDATE users SET display_name = 'Sam', display_key = 'sam'`)
	if _, err := e.login("Sam", goodPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("ambiguous name: %v", err)
	}
	if _, err := e.login("sam-one", goodPassword); err != nil {
		t.Errorf("handle still works: %v", err)
	}
	// A deactivated person's name does not make a name ambiguous.
	e.execSQL(`UPDATE users SET deactivated_at = 1 WHERE username = 'sam-two'`)
	if _, err := e.login("Sam", goodPassword); err != nil {
		t.Errorf("unique among active: %v", err)
	}
}

func TestLoginLimitSharedAcrossNameAndHandle(t *testing.T) {
	e := newEnv(t)
	e.openRegistration()
	if _, err := e.registerName("Ada Lovelace"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		name := "Ada Lovelace"
		if i%2 == 1 {
			name = "ada-lovelace"
		}
		if _, err := e.login(name, "wrong password!"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	_, err := e.login("ADA LOVELACE", goodPassword)
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("6th attempt: %v", err)
	}
	e.clock.Advance(LoginWindow + time.Second)
	if _, err := e.login("Ada Lovelace", goodPassword); err != nil {
		t.Errorf("after window: %v", err)
	}
}

func TestBackfillDisplayKeys(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	e.addUser("bob", RoleMember)
	e.execSQL(`UPDATE users SET display_key = NULL, display_name = '  ÉMILE  Zola ' WHERE username = 'alice'`)
	e.execSQL(`UPDATE users SET display_key = NULL, display_name = 'émile zola' WHERE username = 'bob'`)
	for i := 0; i < 2; i++ {
		if err := e.svc.backfillDisplayKeys(e.ctx); err != nil {
			t.Fatalf("backfill %d: %v", i, err)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE display_key = 'émile zola'`); n != 2 {
		t.Errorf("%d rows keyed, want 2 (duplicates tolerated)", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE display_key IS NULL`); n != 0 {
		t.Errorf("%d rows still NULL", n)
	}
}
