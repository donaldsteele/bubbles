package search

import (
	"errors"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// corpus is three users in #general and #random plus a spread of messages over
// three days (2026-03-01 .. 2026-03-03, UTC).
type corpus struct {
	*env
	alice, bob   user
	general, rnd protocol.Channel
	msg          map[string]protocol.Message
}

func newCorpus(t *testing.T) *corpus {
	t.Helper()
	e := newEnv(t)
	c := &corpus{env: e, msg: map[string]protocol.Message{}}
	c.alice = e.human("alice", protocol.RoleMember)
	c.bob = e.human("bob", protocol.RoleMember)
	c.general = e.channel(c.alice, "general", protocol.ChannelPublic)
	c.rnd = e.channel(c.alice, "random", protocol.ChannelPublic)
	e.join(c.bob, c.general)
	e.join(c.bob, c.rnd)

	c.msg["m1"] = e.post(c.alice, c.general.ID, "deploy the build to staging")
	c.msg["m2"] = e.post(c.bob, c.general.ID, "deploy failed see https://ci.example.com/run/1")
	c.msg["m3"] = e.post(c.alice, c.rnd.ID, "deploy party at noon")
	e.clock.Advance(24 * time.Hour)
	c.msg["m4"] = e.post(c.bob, c.general.ID, "deploy fixed", "doc1")
	c.msg["m5"] = e.post(c.alice, c.general.ID, "deploy screenshot", "img1")
	e.clock.Advance(24 * time.Hour)
	c.msg["m6"] = e.post(c.bob, c.rnd.ID, "deploy retro notes http://wiki.example.com/retro")
	return c
}

func TestSearchFiltersCombine(t *testing.T) {
	c := newCorpus(t)
	const (
		m1 = "deploy the build to staging"
		m2 = "deploy failed see https://ci.example.com/run/1"
		m3 = "deploy party at noon"
		m4 = "deploy fixed"
		m5 = "deploy screenshot"
		m6 = "deploy retro notes http://wiki.example.com/retro"
	)
	tests := []struct {
		q    string
		want []string
	}{
		{"deploy", []string{m1, m2, m3, m4, m5, m6}},
		{"DEPLOY", []string{m1, m2, m3, m4, m5, m6}},
		{"deplo", []string{m1, m2, m3, m4, m5, m6}},
		{"deploy in:#general", []string{m1, m2, m4, m5}},
		{"deploy in:general", []string{m1, m2, m4, m5}},
		{"deploy in:#random", []string{m3, m6}},
		{"deploy in:#general in:#random", []string{m1, m2, m3, m4, m5, m6}},
		{"deploy from:@alice", []string{m1, m3, m5}},
		{"deploy from:@BOB", []string{m2, m4, m6}},
		{"deploy from:@alice from:@bob", []string{m1, m2, m3, m4, m5, m6}},
		{"deploy in:#random from:@bob", []string{m6}},
		{"IN:#GENERAL FROM:@BOB deploy", []string{m2, m4}},
		{"deploy has:file", []string{m4, m5}},
		{"deploy has:image", []string{m5}},
		{"deploy has:link", []string{m2, m6}},
		{"deploy has:link in:#general", []string{m2}},
		{"deploy has:file has:image", []string{m5}},
		{"deploy has:file has:link", nil},
		{"deploy after:2026-03-02", []string{m4, m5, m6}},
		{"deploy before:2026-03-02", []string{m1, m2, m3}},
		{"deploy after:2026-03-02 before:2026-03-03", []string{m4, m5}},
		{"deploy after:2026-03-03 before:2026-03-02", nil},
		{"deploy in:#general from:@bob has:file after:2026-03-02 before:2026-03-03", []string{m4}},
		{`"deploy failed"`, []string{m2}},
		{`"failed deploy"`, nil},
		{`"DEPLOY FAILED" in:#general`, []string{m2}},
		{"deploy fix", []string{m4}},
		{"fix deploy", nil},
		{"staging build", []string{m1}},
		{"in:#nope deploy", nil},
		{"in:#general in:#nope deploy", []string{m1, m2, m4, m5}},
		{"from:@nobody deploy", nil},
		{"foo:bar deploy", nil},
	}
	for _, tc := range tests {
		t.Run(tc.q, func(t *testing.T) {
			wantSet(t, tc.q, c.search(c.alice, tc.q), tc.want...)
		})
	}
	// Filter-only queries scan newest first.
	wantBodies(t, "in:#general from:@bob", c.search(c.alice, "in:#general from:@bob"), m4, m2)
	wantBodies(t, "has:image", c.search(c.alice, "has:image"), m5)
	wantBodies(t, "after:2026-03-03", c.search(c.alice, "after:2026-03-03"), m6)
}

func TestSearchResultsAreHydrated(t *testing.T) {
	c := newCorpus(t)
	page := c.search(c.alice, "deploy fixed")
	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(page.Items))
	}
	m := page.Items[0].Message
	if m.ID != c.msg["m4"].ID || m.ChannelID != c.general.ID || m.Author.Username != "bob" {
		t.Errorf("message = %+v", m)
	}
	if len(m.Files) != 1 || m.Files[0].ID != "doc1" || m.Reactions == nil || m.Mentions.UserIDs == nil {
		t.Errorf("message is not fully hydrated: files=%v reactions=%v mentions=%v", m.Files, m.Reactions, m.Mentions)
	}
	if page.NextCursor != nil {
		t.Errorf("next_cursor = %q, want nil", *page.NextCursor)
	}
}

func TestSearchRepliesEditsAndDeletes(t *testing.T) {
	c := newCorpus(t)
	reply := c.reply(c.bob, c.general.ID, c.msg["m1"].ID, "staging looks good")
	page := c.search(c.alice, "staging")
	wantSet(t, "staging", page, "deploy the build to staging", "staging looks good")
	for _, it := range page.Items {
		if it.Message.ID == reply.ID && (it.Message.ThreadRootID == nil || *it.Message.ThreadRootID != c.msg["m1"].ID) {
			t.Errorf("reply thread_root_id = %v, want %s", it.Message.ThreadRootID, c.msg["m1"].ID)
		}
	}

	if _, err := c.chat.EditMessage(c.ctx, c.bob.Actor, c.msg["m4"].ID, "deploy repaired"); err != nil {
		t.Fatal(err)
	}
	wantBodies(t, "old text after edit", c.search(c.alice, "deploy fixed"))
	wantBodies(t, "new text after edit", c.search(c.alice, "repaired"), "deploy repaired")

	if err := c.chat.DeleteMessage(c.ctx, c.alice.Actor, c.msg["m3"].ID); err != nil {
		t.Fatal(err)
	}
	wantBodies(t, "deleted", c.search(c.alice, "party"))
	wantBodies(t, "deleted, filter only", c.search(c.alice, "in:#random from:@alice"))

	c.system(c.general.ID, c.alice.ID, "deploy notice joined")
	wantBodies(t, "system messages", c.search(c.alice, "notice"))
	wantBodies(t, "system messages, filter only", c.search(c.alice, "in:#general from:@alice has:link"))
}

func TestSearchDiacriticsInsensitive(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	ch := e.channel(alice, "general", protocol.ChannelPublic)
	e.post(alice, ch.ID, "Café au lait")
	e.post(alice, ch.ID, "the naïve résumé plain")
	e.post(alice, ch.ID, "plain cafe latte")

	tests := []struct {
		q    string
		want []string
	}{
		{"cafe", []string{"Café au lait", "plain cafe latte"}},
		{"CAFÉ", []string{"Café au lait", "plain cafe latte"}},
		{"café au", []string{"Café au lait"}},
		{"caf", []string{"Café au lait", "plain cafe latte"}},
		{"resume", []string{"the naïve résumé plain"}},
		{"résumé", []string{"the naïve résumé plain"}},
		{"naive", []string{"the naïve résumé plain"}},
		{`"naïve resume"`, []string{"the naïve résumé plain"}},
		{`"naive resume"`, []string{"the naïve résumé plain"}},
		{`"resume naive"`, nil},
	}
	for _, tc := range tests {
		q := tc.q
		wantSet(t, q, e.search(alice, q), tc.want...)
	}
}

func TestSearchOnlyChannelsTheUserIsIn(t *testing.T) {
	e := newEnv(t)
	admin := e.human("root", protocol.RoleAdmin)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	carol := e.human("carol", protocol.RoleMember)
	guest := e.human("gus", protocol.RoleGuest)

	general := e.channel(alice, "general", protocol.ChannelPublic)
	e.join(bob, general)
	secret := e.channel(alice, "secret", protocol.ChannelPrivate, bob, guest)
	elsewhere := e.channel(alice, "elsewhere", protocol.ChannelPrivate)
	dm, _, err := e.chat.StartDM(e.ctx, alice.Actor, []string{bob.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.post(alice, general.ID, "needle in general")
	e.post(alice, secret.ID, "needle in secret")
	e.post(alice, elsewhere.ID, "needle in elsewhere")
	e.post(alice, dm.ID, "needle in dm")

	tests := []struct {
		name string
		who  user
		want []string
	}{
		{"owner of everything", alice, []string{"needle in general", "needle in secret", "needle in elsewhere", "needle in dm"}},
		{"member of some", bob, []string{"needle in general", "needle in secret", "needle in dm"}},
		{"member of none", carol, nil},
		{"admin is not special", admin, nil},
		{"guest sees only the channel it was added to", guest, []string{"needle in secret"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantSet(t, tc.name, e.search(tc.who, "needle"), tc.want...)
			// A filter cannot reach a channel either.
			wantSet(t, tc.name+" with in:", e.search(tc.who, "needle in:#elsewhere"), filter(tc.want, "needle in elsewhere")...)
			wantSet(t, tc.name+" filter-only", e.search(tc.who, "from:@alice"), tc.want...)
		})
	}

	// A public channel the user has not joined is not searched, even though
	// they can read it.
	wantBodies(t, "public, not joined", e.search(carol, "needle in:#general"))

	// Leaving a channel removes its history from the user's search.
	if err := e.chat.LeaveChannel(e.ctx, bob.Actor, general.ID); err != nil {
		t.Fatal(err)
	}
	wantSet(t, "after leaving", e.search(bob, "needle"), "needle in secret", "needle in dm")
	if err := e.chat.RemoveMember(e.ctx, alice.Actor, secret.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	wantSet(t, "after removal", e.search(bob, "needle"), "needle in dm")
	wantBodies(t, "after removal, filter only", e.search(bob, "in:#secret"))

	// Archived channels stay searchable for their members.
	if _, err := e.chat.ArchiveChannel(e.ctx, alice.Actor, secret.ID); err != nil {
		t.Fatal(err)
	}
	wantBodies(t, "archived", e.search(alice, "needle in:#secret"), "needle in secret")
}

// filter returns want without the entries equal to drop.
func filter(want []string, drop string) []string {
	var out []string
	for _, w := range want {
		if w == drop {
			out = append(out, w)
		}
	}
	return out
}

func TestSearchAppActors(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	app := e.addUser("hook", protocol.RoleMember, protocol.KindApp)
	a := e.channel(alice, "alpha", protocol.ChannelPrivate)
	b := e.channel(alice, "beta", protocol.ChannelPrivate)
	c := e.channel(alice, "gamma", protocol.ChannelPrivate)
	e.addMember(app, a)
	e.addMember(app, b)
	e.post(alice, a.ID, "token in alpha")
	e.post(alice, b.ID, "token in beta")
	e.post(alice, c.ID, "token in gamma")

	actor := func(scopes []string, channels []string) chat.Actor {
		return chat.Actor{ID: app.ID, Role: protocol.RoleMember, Kind: protocol.KindApp, Scopes: scopes, ChannelIDs: channels}
	}
	run := func(act chat.Actor) (protocol.SearchPage, error) {
		return e.svc.Search(e.ctx, act, Params{Q: "token"})
	}

	page, err := run(actor([]string{chat.ScopeMessagesRead}, nil))
	if err != nil {
		t.Fatal(err)
	}
	wantSet(t, "member channels only", page, "token in alpha", "token in beta")

	page, err = run(actor([]string{chat.ScopeMessagesRead}, []string{a.ID, c.ID}))
	if err != nil {
		t.Fatal(err)
	}
	wantSet(t, "allowlist", page, "token in alpha")

	page, err = run(actor([]string{chat.ScopeMessagesRead}, []string{}))
	if err != nil {
		t.Fatal(err)
	}
	wantSet(t, "empty allowlist", page)

	for _, scopes := range [][]string{nil, {chat.ScopeChannelsRead}, {chat.ScopeMessagesWrite}} {
		if _, err := run(actor(scopes, nil)); !errors.Is(err, chat.ErrForbidden) {
			t.Errorf("scopes %v: error = %v, want forbidden", scopes, err)
		}
	}
	if _, err := e.svc.Search(e.ctx, chat.Actor{ID: alice.ID, Role: "wizard"}, Params{Q: "token"}); !errors.Is(err, chat.ErrForbidden) {
		t.Errorf("unknown role: error = %v, want forbidden", err)
	}
}

func TestSearchSnippets(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	ch := e.channel(alice, "general", protocol.ChannelPublic)
	e.post(alice, ch.ID, `<script>alert("x")</script> hello there & <b>friends</b>`)
	e.post(alice, ch.ID, "evil \x02forged\x03 marker hello")
	e.post(alice, ch.ID, strings.Repeat("filler words ", 30)+"hello world "+strings.Repeat("more filler ", 30))

	markTag := regexp.MustCompile(`</?mark>`)
	page := e.search(alice, "hello")
	if len(page.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(page.Items))
	}
	for _, it := range page.Items {
		s := it.Snippet
		if strings.ContainsAny(s, "\x02\x03") {
			t.Errorf("snippet leaks marker characters: %q", s)
		}
		if strings.Count(s, "<mark>") != strings.Count(s, "</mark>") || !strings.Contains(s, "<mark>") {
			t.Errorf("snippet marks are not balanced: %q", s)
		}
		if rest := markTag.ReplaceAllString(s, ""); html.EscapeString(html.UnescapeString(rest)) != rest {
			t.Errorf("snippet text is not escaped: %q", s)
		}
	}
	bySnippet := func(sub string) string {
		for _, it := range page.Items {
			if strings.Contains(it.Message.Body, sub) {
				return it.Snippet
			}
		}
		t.Fatalf("no result containing %q", sub)
		return ""
	}
	escaped := bySnippet("<script>")
	for _, want := range []string{"&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;", "<mark>hello</mark>", "&amp;", "&lt;b&gt;friends&lt;/b&gt;"} {
		if !strings.Contains(escaped, want) {
			t.Errorf("snippet %q lacks %q", escaped, want)
		}
	}
	if strings.Contains(escaped, "<script") || strings.Contains(escaped, "<b>") {
		t.Errorf("snippet contains live markup: %q", escaped)
	}
	long := bySnippet("filler words")
	if len(long) >= 400 || !strings.Contains(long, "<mark>hello</mark> world") || !strings.Contains(long, "…") {
		t.Errorf("long snippet should be an excerpt around the match: %q", long)
	}

	// A filter-only hit gets the escaped start of the text, without marks.
	page = e.search(alice, "in:#general has:link")
	wantBodies(t, "has:link", page)
	e.post(alice, ch.ID, "see https://example.com/<x> "+strings.Repeat("tail ", 100))
	page = e.search(alice, "in:#general has:link")
	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(page.Items))
	}
	s := page.Items[0].Snippet
	if !strings.HasPrefix(s, "see https://example.com/&lt;x&gt; tail") || !strings.HasSuffix(s, "…") || strings.Contains(s, "<mark>") {
		t.Errorf("filter-only snippet = %q", s)
	}
}

func TestSearchRenderSnippet(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "a < b", "a &lt; b"},
		{"mark", "x \x02hi\x03 y", "x <mark>hi</mark> y"},
		{"escapes inside marks", "\x02<i>&\x03", "<mark>&lt;i&gt;&amp;</mark>"},
		{"two marks", "\x02a\x03-\x02b\x03", "<mark>a</mark>-<mark>b</mark>"},
		{"unclosed mark is closed", "p \x02q", "p <mark>q</mark>"},
		{"stray close is dropped", "p \x03 q", "p  q"},
		{"quotes", `"it's"`, "&#34;it&#39;s&#34;"},
	}
	for _, tc := range tests {
		if got := renderSnippet(tc.in); got != tc.want {
			t.Errorf("%s: renderSnippet(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestSearchHostileInputNeverErrors(t *testing.T) {
	c := newCorpus(t)
	inputs := []string{
		`"`, `*`, `-`, `NEAR`, `NEAR(deploy staging, 3)`, `:`, `(`, `)`, `^`, `^deploy`, `body_plain:deploy`, `body_plain : deploy`,
		`rank:x`, `rowid:1`, `deploy NOT staging`, `deploy OR nothing`, `deploy AND`, `"deploy`, `deploy"`, `"deploy" "`, `deploy*`, `*deploy`,
		`-deploy`, `deploy -staging`, `{deploy}`, `[deploy]`, `deploy:`, `:deploy`, `'; DROP TABLE messages; --`, `deploy' OR '1'='1`,
		`%`, `_`, `\`, `deploy\`, `in:#general (`, `from:@" OR 1=1`, `in:"`, `from:"`, `has:link "`, strings.Repeat("(", 200), strings.Repeat("deploy ", 36),
		strings.Repeat("a ", 128),
	}
	for _, in := range inputs {
		page, err := c.svc.Search(c.ctx, c.alice.Actor, Params{Q: in})
		if err != nil {
			var ve *chat.ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("Search(%q): %v", in, err)
			}
			continue
		}
		if page.Items == nil {
			t.Errorf("Search(%q): items is nil, want an empty list", in)
		}
	}
	if n := c.count(`SELECT COUNT(*) FROM messages`); n != 6 {
		t.Errorf("messages = %d, want 6", n)
	}
	// Syntax characters are literal: the quoted forms still find the text.
	wantSet(t, "deploy*", c.search(c.alice, "deploy*"), bodies(c.search(c.alice, "deploy"))...)
	wantBodies(t, "NEAR is a word", c.search(c.alice, "NEAR"))
	wantBodies(t, "column filter is a word", c.search(c.alice, "body_plain:deploy"))
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Read().QueryRowContext(e.ctx, query, args...).Scan(&n); err != nil {
		e.t.Fatalf("count: %v", err)
	}
	return n
}

func TestSearchQueryErrors(t *testing.T) {
	c := newCorpus(t)
	for _, in := range []string{"", "   ", "before:soon", "has:video", "after:2026-02-30", "a\x00b"} {
		_, err := c.svc.Search(c.ctx, c.alice.Actor, Params{Q: in})
		var ve *chat.ValidationError
		if !errors.As(err, &ve) || ve.Fields["q"] == "" {
			t.Errorf("Search(%q) error = %v, want a validation error on q", in, err)
		}
	}
}

func TestSearchDatesUseTheConfiguredZone(t *testing.T) {
	c := newCorpus(t)
	// m1..m3 were created on 2026-03-01 around 12:00 UTC, which is 17:00 on the
	// 1st at UTC+5, and the day-2 messages at 17:00 on the 2nd.
	plus5 := New(c.db, c.chat, time.FixedZone("plus5", 5*3600))
	run := func(q string) protocol.SearchPage {
		page, err := plus5.Search(c.ctx, c.alice.Actor, Params{Q: q})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	// Local midnight of 2026-03-02 at UTC+5 is 19:00 UTC on the 1st, after
	// m1..m3 were created and before m4.
	wantSet(t, "before:2026-03-02", run("deploy before:2026-03-02"),
		"deploy the build to staging", "deploy failed see https://ci.example.com/run/1", "deploy party at noon")
	wantSet(t, "after:2026-03-02", run("deploy after:2026-03-02"),
		"deploy fixed", "deploy screenshot", "deploy retro notes http://wiki.example.com/retro")
	// The default (nil) zone is the server's local zone.
	if got := New(c.db, c.chat, nil).loc; got != time.Local {
		t.Errorf("default zone = %v, want time.Local", got)
	}
}

func TestSearchPaginationIsStable(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	ch := e.channel(alice, "pages", protocol.ChannelPublic)
	other := e.channel(alice, "other", protocol.ChannelPublic)
	for i := 0; i < 40; i++ {
		e.post(alice, other.ID, "unrelated chatter "+strconv.Itoa(i))
	}
	const total = 130
	for i := 0; i < total; i++ {
		// Different lengths give different ranks; equal lengths tie.
		e.post(alice, ch.ID, "needle "+strings.Repeat("filler ", i%9)+strconv.Itoa(i))
	}

	collect := func(q string, limit int) ([]protocol.SearchResult, int) {
		t.Helper()
		var out []protocol.SearchResult
		cursor, pages := "", 0
		for {
			page, err := e.svc.Search(e.ctx, alice.Actor, Params{Q: q, Cursor: cursor, Limit: limit})
			if err != nil {
				t.Fatalf("page %d of %q: %v", pages, q, err)
			}
			pages++
			if len(page.Items) > MaxLimit || (limit > 0 && len(page.Items) > limit) {
				t.Fatalf("page %d has %d items, limit %d", pages, len(page.Items), limit)
			}
			out = append(out, page.Items...)
			if page.NextCursor == nil {
				return out, pages
			}
			if len(page.Items) == 0 {
				t.Fatalf("page %d is empty but has a cursor", pages)
			}
			cursor = *page.NextCursor
			if pages > 1000 {
				t.Fatal("pagination does not terminate")
			}
		}
	}
	ids := func(rs []protocol.SearchResult) []string {
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = r.Message.ID
		}
		return out
	}

	for _, q := range []string{"needle in:#pages", "in:#pages"} {
		small, smallPages := collect(q, 7)
		big, bigPages := collect(q, 0)
		if len(small) != total || len(big) != total {
			t.Fatalf("%q: %d / %d results, want %d", q, len(small), len(big), total)
		}
		if smallPages != 19 || bigPages != 3 {
			t.Errorf("%q: pages = %d / %d, want 19 / 3", q, smallPages, bigPages)
		}
		if !slices.Equal(ids(small), ids(big)) {
			t.Errorf("%q: page size changes the order", q)
		}
		seen := map[string]bool{}
		for _, id := range ids(small) {
			if seen[id] {
				t.Fatalf("%q: %s returned twice", q, id)
			}
			seen[id] = true
		}
	}

	// Relevance order: shorter messages first (fewer filler words), newest
	// first among equals.
	ranked, _ := collect("needle", 7)
	lastFiller, lastID := -1, ""
	for _, r := range ranked {
		filler := strings.Count(r.Message.Body, "filler")
		switch {
		case filler < lastFiller:
			t.Fatalf("relevance order broken: %d filler words after %d", filler, lastFiller)
		case filler == lastFiller && r.Message.ID >= lastID:
			t.Fatalf("ties must be newest first: %s after %s", r.Message.ID, lastID)
		}
		lastFiller, lastID = filler, r.Message.ID
	}

	// A filter-only query is newest first, and new messages do not disturb a
	// scan in progress.
	first, err := e.svc.Search(e.ctx, alice.Actor, Params{Q: "in:#pages", Limit: 10})
	if err != nil || first.NextCursor == nil {
		t.Fatalf("first page: %v / %v", first.NextCursor, err)
	}
	e.post(alice, ch.ID, "needle posted during the scan")
	second, err := e.svc.Search(e.ctx, alice.Actor, Params{Q: "in:#pages", Limit: 10, Cursor: *first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range second.Items {
		if r.Message.ID >= first.Items[len(first.Items)-1].Message.ID {
			t.Errorf("second page repeats or precedes the first page: %s", r.Message.ID)
		}
	}
	if len(second.Items) != 10 {
		t.Errorf("second page has %d items, want 10", len(second.Items))
	}

	// Oversized limits are clamped to a page of MaxLimit.
	page, err := e.svc.Search(e.ctx, alice.Actor, Params{Q: "needle", Limit: 500})
	if err != nil || len(page.Items) != MaxLimit {
		t.Errorf("limit 500: %d items, %v; want %d", len(page.Items), err, MaxLimit)
	}
}

func TestSearchCursorValidation(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	ch := e.channel(alice, "general", protocol.ChannelPublic)
	for i := 0; i < 3; i++ {
		e.post(alice, ch.ID, "needle "+strconv.Itoa(i))
	}
	ranked, err := e.svc.Search(e.ctx, alice.Actor, Params{Q: "needle", Limit: 1})
	if err != nil || ranked.NextCursor == nil {
		t.Fatalf("ranked page: %v", err)
	}
	scan, err := e.svc.Search(e.ctx, alice.Actor, Params{Q: "in:#general", Limit: 1})
	if err != nil || scan.NextCursor == nil {
		t.Fatalf("scan page: %v", err)
	}
	tests := []struct{ name, q, cursor string }{
		{"not base64", "needle", "!!!"},
		{"garbage", "needle", "bm90IGEgY3Vyc29y"},
		{"scan cursor on a text query", "needle", *scan.NextCursor},
		{"text cursor on a filter query", "in:#general", *ranked.NextCursor},
		{"too long", "needle", strings.Repeat("A", MaxCursorChars+1)},
	}
	for _, tc := range tests {
		_, err := e.svc.Search(e.ctx, alice.Actor, Params{Q: tc.q, Cursor: tc.cursor})
		var ve *chat.ValidationError
		if !errors.As(err, &ve) || ve.Fields["cursor"] == "" {
			t.Errorf("%s: error = %v, want a validation error on cursor", tc.name, err)
		}
	}
}

// A well-formed cursor that carries the id of a message in a channel the
// actor cannot see is only a position: it leaks nothing about that message.
func TestSearchForeignChannelCursorLeaksNothing(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	open := e.channel(alice, "open", protocol.ChannelPublic)
	e.join(bob, open)
	secret := e.channel(alice, "secret", protocol.ChannelPrivate)
	e.post(alice, open.ID, "needle visible one")
	e.post(alice, open.ID, "needle visible two")
	foreign := e.post(alice, secret.ID, "needle hidden")

	for _, tc := range []struct {
		name, q string
		ranked  bool
	}{
		{"ranked", "needle", true},
		{"scan", "from:@alice", false},
	} {
		for _, rank := range []float64{0, -1e9, 1e9} {
			cur := encodeCursor(tc.ranked, rank, foreign.ID)
			page, err := e.svc.Search(e.ctx, bob.Actor, Params{Q: tc.q, Cursor: cur})
			if err != nil {
				t.Fatalf("%s rank %v: %v", tc.name, rank, err)
			}
			for _, it := range page.Items {
				if it.Message.ChannelID == secret.ID || it.Message.ID == foreign.ID || strings.Contains(it.Snippet, "hidden") {
					t.Errorf("%s rank %v: cursor leaked %+v", tc.name, rank, it)
				}
			}
		}
	}
}
