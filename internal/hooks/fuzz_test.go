package hooks

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/chat"
)

// FuzzMrkdwn: whatever a sender writes, the Markdown that comes out is valid
// text that carries no HTML, no image, no mention and no link but http,
// https and mailto ones.
func FuzzMrkdwn(f *testing.F) {
	for _, s := range []string{
		"*bold* _italic_ ~strike~",
		"<https://a.example/x|text> <mailto:a@b.example> <javascript:alert(1)|x>",
		"<!channel> <!here> <@U1> <#C1|general> <!subteam^S1|@g> <!date^1^{date}|d>",
		"&lt;b&gt; &amp;lt; &amp; &copy;",
		"!<https://a.example|img> ![x](http://e) [x](javascript:alert(1))",
		"`code <b>` ```\nblock\n``` `unclosed",
		"**Firing** ~~gone~~ snake_case_name 2*3*4 # head > quote",
		"<", ">", "<>", "<|>", "<<>>", "&", "&#", "*", "_", "~", "\\", "`", "```",
		"a\x00b\r\nc\xff",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkSafe(t, Mrkdwn(s))
		checkSafe(t, Plain(s))
	})
}

// FuzzSlackPayload: any document either is refused as a validation error or
// translates to a message that fits the chat limits and passes the same
// safety checks, with a clean sender name.
func FuzzSlackPayload(f *testing.F) {
	for _, fx := range fixtures {
		doc, err := DecodeBody(fx.contentType, readFixture(f, fx.file))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(doc)
	}
	for _, s := range []string{
		`{"text":"hi"}`,
		`{"text":"<!channel> hi","username":"a‮b"}`,
		`{"alert":{"severity":"info","title":"t","dedupe_key":"k"}}`,
		`{"blocks":[{"type":"section","text":"plain string"},{"type":"context","elements":[1,null,{}]}]}`,
		`{"attachments":[null,1,"x",{"fields":[{"title":1,"value":true}],"ts":1e300,"color":"#"}]}`,
		`[]`, `null`, `{}`, ``, `{"text":`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		m, err := Translate(doc)
		if err != nil {
			return
		}
		if m.Alert != nil {
			if m.Body != "" || m.Username != "" {
				t.Errorf("alert with body %q username %q", m.Body, m.Username)
			}
			return
		}
		if strings.TrimSpace(m.Body) == "" {
			t.Errorf("empty body accepted for %q", doc)
		}
		if n := utf8.RuneCountInString(m.Body); n > chat.MaxBodyChars {
			t.Errorf("body has %d characters", n)
		}
		checkSafe(t, m.Body)
		if n := utf8.RuneCountInString(m.Username); n > MaxUsernameChars || CleanUsername(m.Username) != m.Username {
			t.Errorf("username %q is not clean", m.Username)
		}
	})
}
