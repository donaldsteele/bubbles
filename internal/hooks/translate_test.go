package hooks

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/chat"
)

var update = flag.Bool("update", false, "rewrite the golden Markdown files in testdata")

// fixtures maps a testdata file to the content type it is sent with.
var fixtures = []struct {
	file, contentType, username string
}{
	{"uptime-kuma-down.json", "application/json", "Uptime Kuma"},
	{"uptime-kuma-up.json", "application/json", "Uptime Kuma"},
	{"grafana-alert.json", "application/json", "Grafana"},
	{"home-assistant.json", "application/json", "Home Assistant"},
	{"slack-blocks.json", "application/json", "Deploy Bot"},
	{"slack-attachments.json", "application/json", "Backup Monitor"},
	{"form-payload.txt", "application/x-www-form-urlencoded", "Backup"},
}

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func translateFixture(t testing.TB, file, contentType string) Message {
	t.Helper()
	doc, err := DecodeBody(contentType, readFixture(t, file))
	if err != nil {
		t.Fatalf("%s: DecodeBody: %v", file, err)
	}
	m, err := Translate(doc)
	if err != nil {
		t.Fatalf("%s: Translate: %v", file, err)
	}
	return m
}

func TestHooksGoldenMarkdown(t *testing.T) {
	for _, fx := range fixtures {
		t.Run(fx.file, func(t *testing.T) {
			m := translateFixture(t, fx.file, fx.contentType)
			if m.Alert != nil {
				t.Fatal("a Slack payload became an alert")
			}
			if m.Username != fx.username {
				t.Errorf("username = %q, want %q", m.Username, fx.username)
			}
			golden := filepath.Join("testdata", strings.TrimSuffix(fx.file, filepath.Ext(fx.file))+".golden.md")
			if *update {
				if err := os.WriteFile(golden, []byte(m.Body+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got := m.Body + "\n"; got != string(want) {
				t.Errorf("Markdown differs from %s (run with -update to rewrite):\n--- got\n%s\n--- want\n%s", golden, got, want)
			}
			checkSafe(t, m.Body)
		})
	}
}

// TestHooksGoldenMentionsAreInert checks the fixtures that carry mention
// syntax: the tokens are gone and no payload text survives as a live link or
// HTML.
func TestHooksGoldenMentionsAreInert(t *testing.T) {
	m := translateFixture(t, "slack-blocks.json", "application/json")
	for _, bad := range []string{"<!channel", "<!here", "<!everyone", "<@U", "<#C", "<!subteam", "javascript:alert(1)>", "](<javascript", "](<data", "<script", "evil.example", "![", "[click"} {
		if strings.Contains(m.Body, bad) {
			t.Errorf("body contains %q:\n%s", bad, m.Body)
		}
	}
	for _, want := range []string{"click me", "(unsupported block)", ":rocket:", "[the notes](<https://git.home.arpa/releases/2.4.0>)", "[mail ops](<mailto:ops@home.arpa>)"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, m.Body)
		}
	}
	if strings.Contains(m.Body, "Fallback text") {
		t.Error("the fallback text is shown next to blocks")
	}
}

func TestHooksMrkdwn(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"bold", "*bold*", "**bold**"},
		{"italic", "_it_", "*it*"},
		{"strike", "~s~", "~~s~~"},
		{"nested", "*b _i_ b*", "**b *i* b**"},
		{"markdown-style double", "**Firing** and ~~gone~~", "**Firing** and ~~gone~~"},
		{"inside word", "snake_case_name 2*3*4", `snake\_case\_name 2\*3\*4`},
		{"loose marker", "a * b", `a \* b`},
		{"span ends at line", "*a\nb*", "\\*a\nb\\*"},
		{"link with text", "<https://a.example/x|text>", "[text](<https://a.example/x>)"},
		{"bare link", "<https://a.example/a_b>", `[https://a.example/a\_b](<https://a.example/a_b>)`},
		{"link entities", "<https://a.example/?a=1&amp;b=2|q>", "[q](<https://a.example/?a=1&b=2>)"},
		{"link entity-like query", "<https://a.example/?a=1&amp;copy;=2|q>", `[q](<https://a.example/?a=1\&copy;=2>)`},
		{"mailto", "<mailto:a@b.example|mail>", "[mail](<mailto:a@b.example>)"},
		{"uppercase scheme", "<HTTPS://a.example|up>", "[up](<HTTPS://a.example>)"},
		{"javascript with label", "<javascript:alert(1)|click>", "click"},
		{"javascript bare", "<JavaScript:alert(1)>", "JavaScript:alert(1)"},
		{"data url", "<data:text/html,hi>", "data:text/html,hi"},
		{"other scheme", "<ftp://x.example|y>", "y"},
		{"not a link", "<div>", `\<div\>`},
		{"unterminated", "a < b", `a \< b`},
		{"empty token", "<>", `\<\>`},
		{"image bang", "!<https://x.example|y>", `\![y](<https://x.example>)`},
		{"entities", "1 &lt; 2 &amp;&amp; 3 &gt; 2", `1 \< 2 && 3 \> 2`},
		{"entity stays literal", "&amp;lt; &copy; AT&T", `\&lt; \&copy; AT&T`},
		{"user mention", "a <@U123> b <@U123|dave> c", "a  b  c"},
		{"channel mention", "<#C1|general> <!channel> <!here> <!everyone> <!subteam^S1|@grp>", ""},
		{"date fallback", "<!date^1700000000^{date_short}|Oct 11>", "Oct 11"},
		{"plain at-mentions", "@channel @here @someuser", "@channel @here @someuser"},
		{"heading", "# not a heading", `\# not a heading`},
		{"markdown link", "see [x](http://evil.example)", `see \[x\](http://evil.example)`},
		{"backslash", `C:\temp`, `C:\\temp`},
		{"inline code", "`*not bold* <b>`", "`*not bold* <b>`"},
		{"code entities", "`1 &lt; 2`", "`1 < 2`"},
		{"unclosed code", "a `b", "a \\`b"},
		{"code block", "x\n```\nline <!here>\n```\ny", "x\n```\nline <!here>\n```\ny"},
		{"quote kept", "> quoted", "> quoted"},
		{"controls", "a\x00b\x07c\r\nd", "abc\nd"},
		{"invalid utf8", "a\xffb", "a\uFFFDb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Mrkdwn(tc.in); got != tc.want {
				t.Errorf("Mrkdwn(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestHooksPlainIsLiteral(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[FIRING:1] *High* CPU_load", `\[FIRING:1\] \*High\* CPU\_load`},
		{"> not a quote `x`", "\\> not a quote \\`x\\`"},
		{"a &amp; b &lt;c&gt; <@U1>", `a & b \<c\> `},
		{"<https://a.example|a link>", "[a link](<https://a.example>)"},
	}
	for _, tc := range cases {
		if got := Plain(tc.in); got != strings.TrimSpace(tc.want) {
			t.Errorf("Plain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHooksMrkdwnIsBounded(t *testing.T) {
	// Pathological inputs stay fast and inside the limits.
	for _, in := range []string{
		strings.Repeat("*a ", 40000),
		strings.Repeat("`a", 40000),
		strings.Repeat("<a", 40000),
		strings.Repeat("```", 20000),
		strings.Repeat("_a*", 40000),
		strings.Repeat("&amp;", 40000),
	} {
		out := Mrkdwn(in)
		if len(out) > 4*maxTextBytes {
			t.Errorf("output of %.10q... has %d bytes", in, len(out))
		}
		checkSafe(t, out)
	}
}

func TestHooksUsername(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Uptime Kuma", "Uptime Kuma"},
		{"  padded \t name  ", "padded name"},
		{"evil\u202eadmin", "eviladmin"},
		{"bell\x07 and\nnewline", "bell and newline"},
		{"zero\u200bwidth", "zerowidth"},
		{strings.Repeat("x", 100), strings.Repeat("x", MaxUsernameChars)},
		{strings.Repeat("é", 100), strings.Repeat("é", MaxUsernameChars)},
		{"ok\xffbad", "okbad"},
		{"\u0007\u202e", ""},
	}
	for _, tc := range cases {
		got := CleanUsername(tc.in)
		if got != tc.want || utf8.RuneCountInString(got) > MaxUsernameChars {
			t.Errorf("CleanUsername(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHooksDecodeBody(t *testing.T) {
	json := `{"text":"hi"}`
	got, err := DecodeBody("application/json; charset=utf-8", []byte(json))
	if err != nil || string(got) != json {
		t.Errorf("json: %q, %v", got, err)
	}
	got, err = DecodeBody("application/x-www-form-urlencoded", []byte("token=x&payload=%7B%22text%22%3A%22hi+there%22%7D"))
	if err != nil || string(got) != `{"text":"hi there"}` {
		t.Errorf("form: %q, %v", got, err)
	}
	for _, ct := range []string{"", "text/plain", "multipart/form-data; boundary=x", "application/xml", "garbage;;"} {
		if _, err := DecodeBody(ct, []byte(json)); !errors.Is(err, ErrUnsupportedMedia) {
			t.Errorf("content type %q: err = %v, want ErrUnsupportedMedia", ct, err)
		}
	}
	for _, body := range []string{"", "text=hi", "payload=", "%zz=1"} {
		_, err := DecodeBody("application/x-www-form-urlencoded", []byte(body))
		var ve *chat.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("form body %q: err = %v, want a validation error", body, err)
		}
	}
}

func TestHooksTranslateRejectsBadPayloads(t *testing.T) {
	for _, tc := range []struct{ name, doc string }{
		{"empty", ``},
		{"not json", `hello`},
		{"array", `[1,2]`},
		{"string", `"hi"`},
		{"two documents", `{"text":"a"} {"text":"b"}`},
		{"empty object", `{}`},
		{"only whitespace", `{"text":"  \n "}`},
		{"only icon", `{"icon_emoji":":smile:"}`},
		{"only mentions", `{"text":"<!channel> <@U1>"}`},
		{"alert not an object", `{"alert":"boom"}`},
		{"alert bad shape", `{"alert":{"severity":5}}`},
	} {
		_, err := Translate([]byte(tc.doc))
		var ve *chat.ValidationError
		if !errors.As(err, &ve) || ve.Fields["payload"] == "" {
			t.Errorf("%s: err = %v, want a validation error on payload", tc.name, err)
		}
	}
}

func TestHooksTranslateNativeAlert(t *testing.T) {
	m, err := Translate([]byte(`{"text":"fallback text","username":"ignored","alert":{"severity":"critical","title":"Disk full","fields":[{"name":"Host","value":"nas","short":true}],"link":"https://nas.home.arpa","dedupe_key":"disk-nas","renotify_after_s":600}}`))
	if err != nil {
		t.Fatal(err)
	}
	a := m.Alert
	if a == nil || m.Body != "" || a.Severity != "critical" || a.Title != "Disk full" || a.Text != "fallback text" || a.DedupeKey != "disk-nas" ||
		a.Link == nil || *a.Link != "https://nas.home.arpa" || a.RenotifyAfterS == nil || *a.RenotifyAfterS != 600 ||
		len(a.Fields) != 1 || a.Fields[0].Name != "Host" || !a.Fields[0].Short {
		t.Fatalf("alert = %+v, body %q", a, m.Body)
	}
	// The alert's own text wins over the top-level text, and a null alert is a plain message.
	m, err = Translate([]byte(`{"text":"top","alert":{"severity":"info","title":"t","text":"own","dedupe_key":"k"}}`))
	if err != nil || m.Alert == nil || m.Alert.Text != "own" {
		t.Errorf("own text: %+v, %v", m.Alert, err)
	}
	m, err = Translate([]byte(`{"text":"*hi*","alert":null}`))
	if err != nil || m.Alert != nil || m.Body != "**hi**" {
		t.Errorf("null alert: %+v, %v", m, err)
	}
}

func TestHooksTranslateTruncatesLongMessages(t *testing.T) {
	text := strings.Repeat("word ", 5000) + "`code" // 25000 chars
	m, err := Translate([]byte(`{"text":"` + text + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(m.Body); n > chat.MaxBodyChars {
		t.Errorf("body has %d characters", n)
	}
	// A fence the cut leaves open is closed.
	long := "```\n" + strings.Repeat("line\n", 4000) + "```"
	m, err = Translate([]byte(`{"text":` + jsonString(long) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(m.Body, "```"); n%2 != 0 || utf8.RuneCountInString(m.Body) > chat.MaxBodyChars {
		t.Errorf("fences = %d, chars = %d", n, utf8.RuneCountInString(m.Body))
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func TestHooksColorMarker(t *testing.T) {
	cases := map[string]string{
		"good": "\U0001F7E2", "warning": "\U0001F7E1", "danger": "\U0001F534", " GOOD ": "\U0001F7E2",
		"#36a64f": "\U0001F7E2", "#2EB886": "\U0001F7E2", "#D63232": "\U0001F534", "#e01e5a": "\U0001F534",
		"#ECB22E": "\U0001F7E1", "#f00": "\U0001F534", "#439FE0": "", "": "", "#12": "", "#gggggg": "", "red": "",
	}
	for in, want := range cases {
		if got := colorMark(in); got != want {
			t.Errorf("colorMark(%q) = %q, want %q", in, got, want)
		}
	}
}

// checkSafe asserts what the translation promises about its output: valid
// text and, outside code, no HTML, no mention token, no image and no link
// that is not http, https or mailto.
func checkSafe(t testing.TB, body string) {
	t.Helper()
	if !utf8.ValidString(body) {
		t.Errorf("invalid UTF-8: %q", body)
	}
	for _, r := range body {
		if r < ' ' && r != '\n' && r != '\t' {
			t.Errorf("control character %q in %q", r, body)
			return
		}
	}
	if strings.Contains(body, "`") {
		return // code is literal; the checks below read the Markdown outside it
	}
	for i := strings.Index(body, "!["); i >= 0; {
		if !escaped(body, i) {
			t.Errorf("image syntax in %q", body)
			return
		}
		next := strings.Index(body[i+1:], "![")
		if next < 0 {
			break
		}
		i += 1 + next
	}
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '<':
			if escaped(body, i) {
				continue
			}
			if !strings.HasSuffix(body[:i], "](") {
				t.Errorf("raw < at %d in %q", i, body)
				return
			}
			dest := body[i+1:]
			end := strings.IndexByte(dest, '>')
			low := strings.ToLower(dest)
			if end < 0 || !(strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") || strings.HasPrefix(low, "mailto:")) {
				t.Errorf("link destination %.40q in %q", dest, body)
				return
			}
		case '[':
			if !escaped(body, i) && !strings.Contains(body[i:], "](<") {
				t.Errorf("raw [ at %d in %q", i, body)
				return
			}
		}
	}
}

// escaped reports whether the byte at i follows an odd number of backslashes.
func escaped(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}
