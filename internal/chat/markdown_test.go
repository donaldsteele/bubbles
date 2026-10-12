package chat

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestExtractMentions(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Mentions
	}{
		{"plain", "hello @alice and @Bob.", Mentions{Usernames: []string{"alice", "bob"}}},
		{"duplicates collapse", "@alice @ALICE @alice", Mentions{Usernames: []string{"alice"}}},
		{"channel and here", "@channel meeting, @here please", Mentions{Channel: true, Here: true}},
		{"start of text and punctuation", "(@alice), \"@bob\"", Mentions{Usernames: []string{"alice", "bob"}}},
		{"email is not a mention", "mail alice@example.com", Mentions{}},
		{"path is not a mention", "see /home/@alice and https://x.test/@bob", Mentions{}},
		{"escaped", `\@alice`, Mentions{}},
		{"too short", "@a", Mentions{}},
		{"trailing dot is punctuation", "thanks @alice.", Mentions{Usernames: []string{"alice"}}},
		{"channels is not channel", "@channels @heres", Mentions{Usernames: []string{"channels", "heres"}}},
		{"inline code", "run `@alice` now", Mentions{}},
		{"double backtick code", "run ``@alice `x` @bob`` now", Mentions{}},
		{"fenced code", "```\n@alice @channel @here\n```", Mentions{}},
		{"fenced code with info", "```@alice\ncode\n```\n@bob", Mentions{Usernames: []string{"bob"}}},
		{"tilde fence", "~~~\n@alice\n~~~", Mentions{}},
		{"indented code", "text\n\n    @alice\n\n@bob", Mentions{Usernames: []string{"bob"}}},
		{"mention next to code", "@alice `@bob` @carol", Mentions{Usernames: []string{"alice", "carol"}}},
		{"in emphasis", "**@alice** and _@bob_", Mentions{Usernames: []string{"alice", "bob"}}},
		{"in quote and list", "> @alice\n\n- @bob\n- @here", Mentions{Usernames: []string{"alice", "bob"}, Here: true}},
		{"underscore in name", "@bob_x ok", Mentions{Usernames: []string{"bob_x"}}},
		{"inline html tags are text", "<b>@alice</b>", Mentions{Usernames: []string{"alice"}}},
		{"html block ignored", "<div>@alice</div>\n\n@bob", Mentions{Usernames: []string{"bob"}}},
		{"after intraword underscore", "foo_@alice", Mentions{}},
		{"autolink", "<https://x.test/@alice>", Mentions{}},
	}
	for _, c := range cases {
		got := ExtractMentions(c.body)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: ExtractMentions(%q) = %+v, want %+v", c.name, c.body, got, c.want)
		}
	}
}

func TestMentionCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < MaxMentionedUsers+20; i++ {
		b.WriteString("@user")
		b.WriteString(strings.Repeat("a", i%10))
		b.WriteString(string(rune('a' + i/10)))
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(" ")
	}
	if got := ExtractMentions(b.String()); len(got.Usernames) > MaxMentionedUsers {
		t.Fatalf("%d usernames, want at most %d", len(got.Usernames), MaxMentionedUsers)
	}
}

func TestPlainTextKeepsCodeForSearch(t *testing.T) {
	body := "# Title\n\nSome **bold** and `inline_code` with [a link](https://x.test/p).\n\n```go\nfmt.Println(\"needle\")\n```\n\n- item one\n- item two\n"
	plain := parseBody(body).Plain
	for _, want := range []string{"Title", "bold", "inline_code", "a link", "needle", "item one", "item two"} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain text %q lacks %q", plain, want)
		}
	}
	for _, bad := range []string{"**", "```", "](", "# "} {
		if strings.Contains(plain, bad) {
			t.Errorf("plain text %q still contains markup %q", plain, bad)
		}
	}
}

var validUsername = regexp.MustCompile(`^[a-z0-9._-]{2,32}$`)

func FuzzExtractMentions(f *testing.F) {
	for _, seed := range []string{
		"", "@", "@@", "@alice", "hi @alice.", "`@alice`", "```\n@bob\n```", "    @code", "a@b.c", "\\@x",
		"**@a_b**", "> @alice\n> `@bob`", "[@alice](http://x/@bob)", "<b>@x</b>", "@channel @here", "@éé",
		"~~~\n@x\n~~~", "- @a\n  - @b", "``@a``", "\x00@alice", "@alice@bob", "@a_b_c_d_e_f_g_h_i_j_k_l_m_n_o_p_q_r_s_t",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		got := ExtractMentions(body)
		seen := map[string]bool{}
		for _, u := range got.Usernames {
			if !validUsername.MatchString(u) {
				t.Fatalf("username %q from %q does not look like a username", u, body)
			}
			if seen[u] {
				t.Fatalf("duplicate username %q from %q", u, body)
			}
			seen[u] = true
			if u == "channel" || u == "here" {
				t.Fatalf("%q reported as a username", u)
			}
		}
		if len(got.Usernames) > MaxMentionedUsers {
			t.Fatalf("%d usernames from %q", len(got.Usernames), body)
		}
		if !strings.Contains(body, "@") && (len(got.Usernames) > 0 || got.Channel || got.Here) {
			t.Fatalf("mentions %+v without an @ in %q", got, body)
		}
		// Whatever the text, one backtick span around it is code.
		inner := strings.NewReplacer("`", "", "\n", " ", "\r", " ").Replace(body)
		if !strings.Contains(inner, "\x00") {
			wrapped := ExtractMentions("`" + inner + "`")
			if len(wrapped.Usernames) > 0 || wrapped.Channel || wrapped.Here {
				t.Fatalf("mentions %+v inside a code span around %q", wrapped, inner)
			}
		}
	})
}

func TestPlainTextStripsC0Controls(t *testing.T) {
	plain := parseBody("a\x02b\x03c\x00d\re\x1bf\ng\th").Plain
	if want := "abcdef\ng\th"; plain != want {
		t.Errorf("plain = %q, want %q", plain, want)
	}
}
