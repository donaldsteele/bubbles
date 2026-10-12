package httpapi

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/internal/search"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func searchPath(q string, extra ...string) string {
	v := url.Values{"q": {q}}
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	return "/api/v1/search?" + v.Encode()
}

func (m member) search(t *testing.T, q string, extra ...string) protocol.SearchPage {
	t.Helper()
	rec := m.do(http.MethodGet, searchPath(q, extra...), nil)
	wantStatus(t, rec, http.StatusOK, "")
	return decodeBody[protocol.SearchPage](t, rec)
}

func searchIDs(p protocol.SearchPage) []string {
	out := make([]string, len(p.Items))
	for i, it := range p.Items {
		out[i] = it.Message.ID
	}
	return out
}

func TestSearchRouteRequiresSignIn(t *testing.T) {
	e := newEnv(t)
	e.setup()
	wantStatus(t, e.client("").do(http.MethodGet, searchPath("hello"), nil), http.StatusUnauthorized, CodeUnauthorized)
}

func TestSearchFindsMessagesWithSnippets(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	bob := e.newMember("bob", "192.168.1.51")
	team := admin.createChannel(t, "team", "public", bob)
	m1 := admin.post(t, team.ID, "ship the **release** notes today")
	m2 := bob.post(t, team.ID, "release candidate ready, see https://ci.example.com/1")
	m3 := bob.post(t, team.ID, "lunch anyone?")

	page := bob.search(t, "release")
	if got := searchIDs(page); len(got) != 2 || !slices.Contains(got, m1.ID) || !slices.Contains(got, m2.ID) {
		t.Fatalf("ids = %v, want %s and %s", got, m1.ID, m2.ID)
	}
	if page.NextCursor != nil {
		t.Errorf("next_cursor = %q, want null", *page.NextCursor)
	}
	for _, it := range page.Items {
		if !strings.Contains(it.Snippet, "<mark>release</mark>") {
			t.Errorf("snippet %q lacks the highlight", it.Snippet)
		}
		if it.Message.ChannelID != team.ID || it.Message.Author.Username == "" {
			t.Errorf("message not hydrated: %+v", it.Message)
		}
	}

	// Filters combine; the grammar is case-insensitive.
	wantIDs := func(what string, p protocol.SearchPage, want ...string) {
		t.Helper()
		if got := searchIDs(p); !slices.Equal(got, want) {
			t.Errorf("%s: ids = %v, want %v", what, got, want)
		}
	}
	wantIDs("from+has", bob.search(t, "release FROM:@bob HAS:link in:#team"), m2.ID)
	wantIDs("phrase", bob.search(t, `"release notes"`), m1.ID)
	wantIDs("prefix", bob.search(t, "candi"), m2.ID)
	wantIDs("filter only, newest first", bob.search(t, "in:#team from:@bob"), m3.ID, m2.ID)
	wantIDs("unknown channel", bob.search(t, "release in:#nowhere"))
	wantIDs("date", bob.search(t, "release after:2026-03-02"))
	wantIDs("date, same day", bob.search(t, "release after:2026-03-01 before:2026-03-02 from:@bob"), m2.ID)
}

func TestSearchNeverLeaksOtherChannels(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	alice := e.newMember("alice", "192.168.1.51")
	bob := e.newMember("bob", "192.168.1.52")
	carol := e.newMember("carol", "192.168.1.53")

	vault := alice.createChannel(t, "vault", "private", bob)
	alice.post(t, vault.ID, "needle in the vault")
	dm := decodeBody[protocol.Channel](t, alice.do(http.MethodPost, "/api/v1/dms", protocol.StartDMRequest{UserIDs: []string{bob.id}}))
	alice.post(t, dm.ID, "needle in the dm")
	open := alice.createChannel(t, "open", "public", bob)
	alice.post(t, open.ID, "needle in the open")

	if n := len(alice.search(t, "needle").Items); n != 3 {
		t.Errorf("alice sees %d, want 3", n)
	}
	if n := len(bob.search(t, "needle").Items); n != 3 {
		t.Errorf("bob sees %d, want 3", n)
	}
	// Carol, and the admin, are in none of them (the open channel is public
	// but they have not joined it).
	for name, who := range map[string]member{"carol": carol, "admin": admin} {
		if n := len(who.search(t, "needle").Items); n != 0 {
			t.Errorf("%s sees %d results, want 0", name, n)
		}
		if n := len(who.search(t, "needle in:#vault").Items); n != 0 {
			t.Errorf("%s sees %d results with in:#vault, want 0", name, n)
		}
		if n := len(who.search(t, "from:@alice").Items); n != 0 {
			t.Errorf("%s sees %d results with from:@alice, want 0", name, n)
		}
	}
	// Leaving removes a channel from the results.
	wantStatus(t, bob.do(http.MethodPost, "/api/v1/channels/"+open.ID+"/leave", nil), http.StatusNoContent, "")
	if n := len(bob.search(t, "needle").Items); n != 2 {
		t.Errorf("bob sees %d after leaving, want 2", n)
	}
}

func TestSearchPaginatesWithCursor(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	ch := admin.createChannel(t, "pages", "public")
	var posted []string
	for i := 0; i < 5; i++ {
		posted = append(posted, admin.post(t, ch.ID, "pager entry "+strconv.Itoa(i)).ID)
	}
	for _, q := range []string{"pager", "in:#pages"} {
		var got []string
		cursor, pages := "", 0
		for {
			extra := []string{"limit", "2"}
			if cursor != "" {
				extra = append(extra, "cursor", cursor)
			}
			page := admin.search(t, q, extra...)
			pages++
			got = append(got, searchIDs(page)...)
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
		}
		if pages != 3 || len(got) != 5 {
			t.Errorf("%q: %d pages, %d results; want 3 and 5", q, pages, len(got))
		}
		slices.Sort(got)
		if !slices.Equal(got, posted) {
			t.Errorf("%q: ids = %v, want %v", q, got, posted)
		}
	}
	// Default and maximum page size.
	if n := len(admin.search(t, "pager").Items); n != 5 {
		t.Errorf("default page has %d items, want 5", n)
	}
	if n := len(admin.search(t, "pager", "limit", "50").Items); n != 5 {
		t.Errorf("limit=50 page has %d items, want 5", n)
	}
}

func TestSearchSnippetIsEscapedHTML(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	ch := admin.createChannel(t, "xss", "public")
	admin.post(t, ch.ID, `<script>alert("pwned")</script> findme <img src=x onerror=alert(1)> & done`)
	page := admin.search(t, "findme")
	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(page.Items))
	}
	s := page.Items[0].Snippet
	if strings.Contains(s, "<script") || strings.Contains(s, "<img") {
		t.Errorf("snippet carries live markup: %q", s)
	}
	for _, want := range []string{"&lt;script&gt;", "<mark>findme</mark>", "&lt;img", "&amp; done"} {
		if !strings.Contains(s, want) {
			t.Errorf("snippet %q lacks %q", s, want)
		}
	}
}

func TestSearchRejectsBadParameters(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	tests := []struct {
		name  string
		path  string
		field string
	}{
		{"no q", "/api/v1/search", "q"},
		{"empty q", "/api/v1/search?q=", "q"},
		{"blank q", "/api/v1/search?q=%20%20", "q"},
		{"repeated q", "/api/v1/search?q=a&q=b", "q"},
		{"only punctuation", searchPath(`- ( ) *`), "q"},
		{"too long", searchPath(strings.Repeat("a", search.MaxQueryChars+1)), "q"},
		{"bad date", searchPath("x before:tomorrow"), "q"},
		{"impossible date", searchPath("x after:2026-02-30"), "q"},
		{"bad has", searchPath("x has:pdf"), "q"},
		{"NUL", "/api/v1/search?q=a%00b", "q"},
		{"limit zero", searchPath("x", "limit", "0"), "limit"},
		{"limit too big", searchPath("x", "limit", "51"), "limit"},
		{"limit text", searchPath("x", "limit", "many"), "limit"},
		{"repeated limit", searchPath("x") + "&limit=1&limit=2", "limit"},
		{"empty cursor", searchPath("x") + "&cursor=", "cursor"},
		{"garbage cursor", searchPath("x", "cursor", "bm90IGEgY3Vyc29y"), "cursor"},
		{"cursor of another kind", searchPath("x", "cursor", "aTo6MDFBUlozTkRFS1RTVjRSUkZGUTY5RzVGQVY"), "cursor"},
		{"oversized cursor", searchPath("x", "cursor", strings.Repeat("A", search.MaxCursorChars+1)), "cursor"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := admin.do(http.MethodGet, tc.path, nil)
			e := wantStatus(t, rec, http.StatusBadRequest, CodeBadRequest)
			if _, ok := e.FieldErrors()[tc.field]; !ok {
				t.Errorf("details.fields = %v, want a reason for %q", e.FieldErrors(), tc.field)
			}
		})
	}
}

func TestSearchHostileQueriesAreNeverServerErrors(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	ch := admin.createChannel(t, "hostile", "public")
	admin.post(t, ch.ID, "NEAR the (parenthesis) and a ^caret: column body_plain -dash *star")
	for _, q := range []string{
		`"`, `*`, `-`, `NEAR`, `NEAR(a b)`, `:`, `(`, `)`, `^`, `body_plain:caret`, `body_plain : x`, `column:body_plain`,
		`a OR b`, `a NOT b`, `"caret" "`, `-dash`, `*star*`, `'; DROP TABLE messages; --`, `rank:1`, `in:#hostile (`, `from:@"`,
	} {
		rec := admin.do(http.MethodGet, searchPath(q), nil)
		if rec.Code != http.StatusOK && rec.Code != http.StatusBadRequest {
			t.Errorf("q=%q: status %d, body %s", q, rec.Code, rec.Body.String())
		}
		if rec.Code == http.StatusOK && rec.Header().Get("Content-Type") == "" {
			t.Errorf("q=%q: no Content-Type", q)
		}
	}
	// FTS syntax is literal text, not syntax.
	if n := len(admin.search(t, "NEAR").Items); n != 1 {
		t.Errorf("q=NEAR finds %d, want 1 (NEAR is an ordinary word)", n)
	}
	if n := len(admin.search(t, "body_plain:caret").Items); n != 0 {
		t.Errorf("column filter found %d, want 0", n)
	}
}
