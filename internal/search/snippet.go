package search

import (
	"html"
	"strings"
	"unicode/utf8"
)

// FTS5 snippet() is asked to wrap matches in these two control characters;
// renderSnippet turns them into <mark> tags after escaping the text.
const (
	markOpen  = "\x02"
	markClose = "\x03"
)

const (
	// snippetTokens is the number of tokens snippet() may return (FTS5 max 64).
	snippetTokens = 16
	// excerptRunes is the length of the excerpt of a filter-only hit.
	excerptRunes = 160
)

// renderSnippet converts the raw output of FTS5 snippet() into an HTML
// fragment: every character of the message text is HTML-escaped and the only
// markup is <mark>...</mark> around each highlighted run. The result is
// well-formed even if the message text itself contains the marker characters.
func renderSnippet(raw string) string {
	var b strings.Builder
	parts := strings.Split(raw, markOpen)
	b.WriteString(escapeText(parts[0]))
	for _, p := range parts[1:] {
		hit, rest, _ := strings.Cut(p, markClose)
		b.WriteString("<mark>")
		b.WriteString(escapeText(hit))
		b.WriteString("</mark>")
		b.WriteString(escapeText(rest))
	}
	return b.String()
}

var markStripper = strings.NewReplacer(markOpen, "", markClose, "")

func escapeText(s string) string {
	return html.EscapeString(markStripper.Replace(s))
}

// excerpt is the snippet of a hit without text terms: the start of the text,
// escaped and without highlights.
func excerpt(text string) string {
	if utf8.RuneCountInString(text) <= excerptRunes {
		return escapeText(text)
	}
	cut := 0
	for n := 0; n < excerptRunes; n++ {
		_, size := utf8.DecodeRuneInString(text[cut:])
		cut += size
	}
	return escapeText(text[:cut]) + "…"
}
