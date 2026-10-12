package alerts

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Render builds the Markdown body of an alert message: the title in bold, the
// text, a two-column table of the fields and a link. The body is plain
// Markdown the clients render with raw HTML disabled; nothing here emits
// HTML. It never exceeds chat.MaxBodyChars: when the parts are too long,
// trailing fields are dropped first, then the text is cut.
func Render(title, text string, fields []protocol.AlertField, link string) string {
	head := "**" + escapeInline(title) + "**"
	tail := ""
	if link != "" {
		tail = "[Open link](<" + link + ">)"
	}
	rows := make([]string, len(fields))
	for i, f := range fields {
		rows[i] = "| " + escapeCell(f.Name) + " | " + escapeCell(f.Value) + " |"
	}
	text = strings.TrimSpace(text)
	nrows := len(rows)
	for {
		body := compose(head, text, rows[:nrows], tail)
		over := utf8.RuneCountInString(body) - chat.MaxBodyChars
		if over <= 0 {
			return body
		}
		switch {
		case nrows > 0:
			nrows--
		case text != "":
			keep := utf8.RuneCountInString(text) - over - 1
			if keep <= 0 {
				text = ""
			} else {
				text = strings.TrimSpace(string([]rune(text)[:keep])) + "…"
			}
		default:
			return string([]rune(body)[:chat.MaxBodyChars])
		}
	}
}

func compose(head, text string, rows []string, tail string) string {
	parts := []string{head}
	if text != "" {
		parts = append(parts, text)
	}
	if len(rows) > 0 {
		parts = append(parts, "| Field | Value |\n| --- | --- |\n"+strings.Join(rows, "\n"))
	}
	if tail != "" {
		parts = append(parts, tail)
	}
	return strings.Join(parts, "\n\n")
}

// escapeInline makes s one line of literal Markdown text.
func escapeInline(s string) string {
	var b strings.Builder
	for _, r := range collapse(s) {
		switch r {
		case '\\', '*', '_', '`', '[', ']', '<', '>', '|', '~':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// escapeCell makes s the content of one table cell.
func escapeCell(s string) string {
	var b strings.Builder
	for _, r := range collapse(s) {
		if r == '\\' || r == '|' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// collapse turns every run of whitespace and control characters into one
// space and trims the result.
func collapse(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// validLink reports whether s is an absolute http or https URL that can be
// written as a Markdown link destination: no blanks, control characters or
// angle brackets, a host, at most protocol.MaxAlertLink characters.
func validLink(s string) bool {
	if s == "" || len(s) > protocol.MaxAlertLink || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '<' || r == '>' {
			return false
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")
}
