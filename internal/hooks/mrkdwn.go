package hooks

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits that keep the translation linear in practice: the longest text
// converted in one call, the longest bold/italic/strike span, link or code
// span searched for, and the deepest nesting of formatting.
const (
	maxTextBytes = 16 << 10
	maxSpanBytes = 2000
	maxDepth     = 4
	maxURLBytes  = 2048
)

// mode selects what the converter interprets.
type mode struct {
	// format turns *bold*, _italic_, ~strike~ and `code` into Markdown; off,
	// the text is literal (Slack titles and plain_text elements).
	format bool
	// links turns <url|text> into a Markdown link; off, the whole token is
	// literal text (a link label cannot hold another link).
	links bool
}

var (
	modeText  = mode{format: true, links: true}
	modePlain = mode{links: true}
	modeLabel = mode{}
)

// Mrkdwn translates Slack mrkdwn into Markdown that is safe to store and show:
//
//	<https://x|text>  ->  [text](<https://x>)   (http, https and mailto only;
//	                                             any other scheme is plain text)
//	<https://x>       ->  [https://x](<https://x>)
//	*bold* _italic_ ~strike~  ->  **bold** *italic* ~~strike~~
//	&lt; &gt; &amp;   ->  < > &, escaped so they stay literal
//	<@U1> <#C1> <!channel> <!here> <!everyone> <!subteam^S1>  ->  removed
//
// Everything else that Markdown would interpret (brackets, backslashes, stray
// emphasis marks, a leading #) is escaped, so no raw HTML, image, link or
// mention can come out of text the sender did not mark as such.
func Mrkdwn(s string) string { return convertTop(s, modeText) }

// Plain translates text Slack shows literally (titles, plain_text elements,
// field names): entities are decoded and links and mentions handled as in
// Mrkdwn, but no formatting is applied.
func Plain(s string) string { return convertTop(s, modePlain) }

func convertTop(s string, m mode) string {
	s = sanitize(clipBytes(s, maxTextBytes))
	return strings.TrimSpace(convert(s, m, 0))
}

// sanitize makes s valid UTF-8 without control characters other than newline
// and tab, with Unix line ends.
func sanitize(s string) string {
	if strings.IndexByte(s, '\r') >= 0 {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
	}
	s = strings.ToValidUTF8(s, "�")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == ' ' || r == ' ':
			return '\n'
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// clipBytes cuts s to at most n bytes at a rune boundary.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func convert(s string, m mode, depth int) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/8)
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case '`':
			if m.format {
				if n := writeCode(&b, s, i); n > 0 {
					i += n
					continue
				}
			}
			b.WriteString("\\`")
			i++
		case '<':
			i += writeAngle(&b, s, i, m, depth)
		case '&':
			i += writeEntity(&b, s, i)
		case '*', '_', '~':
			if m.format && depth < maxDepth {
				if n := writeSpan(&b, s, i, m, depth); n > 0 {
					i += n
					continue
				}
			}
			b.WriteByte('\\')
			b.WriteByte(c)
			i++
		case '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
			i++
		case '!':
			// "!" in front of a link would make it an image.
			if i+1 < len(s) && s[i+1] == '<' {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
			i++
		case '#':
			if i == 0 || s[i-1] == '\n' {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
			i++
		case '>':
			if !m.format {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// writeCode writes the inline code span or code block that starts at s[i] and
// returns the bytes consumed, or 0 when the backticks are not closed.
func writeCode(b *strings.Builder, s string, i int) int {
	rest := s[i:]
	if strings.HasPrefix(rest, "```") {
		window := rest[3:min(len(rest), 3+maxTextBytes)]
		j := strings.Index(window, "```")
		if j < 0 {
			return 0
		}
		code := strings.Trim(decodeEntities(window[:j]), "\n")
		if b.Len() > 0 && !endsWithNewline(b) {
			b.WriteByte('\n')
		}
		b.WriteString("```\n")
		b.WriteString(code)
		b.WriteString("\n```\n")
		n := 3 + j + 3
		if n < len(rest) && rest[n] == '\n' {
			n++
		}
		return n
	}
	if strings.HasPrefix(rest, "``") {
		return 0
	}
	window := rest[1:min(len(rest), 1+maxSpanBytes)]
	j := strings.IndexByte(window, '`')
	if j <= 0 || strings.IndexByte(window[:j], '\n') >= 0 {
		return 0
	}
	b.WriteByte('`')
	b.WriteString(decodeEntities(window[:j]))
	b.WriteByte('`')
	return 1 + j + 1
}

func endsWithNewline(b *strings.Builder) bool {
	s := b.String()
	return s != "" && s[len(s)-1] == '\n'
}

// decodeEntities decodes the three entities Slack defines.
func decodeEntities(s string) string {
	if strings.IndexByte(s, '&') < 0 {
		return s
	}
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(s)
}

// writeEntity writes the text for s[i] == '&' and returns the bytes consumed.
// The three Slack entities are decoded; an ampersand that would start an HTML
// entity in Markdown is escaped.
func writeEntity(b *strings.Builder, s string, i int) int {
	rest := s[i:]
	switch {
	case strings.HasPrefix(rest, "&lt;"):
		b.WriteString("\\<")
		return 4
	case strings.HasPrefix(rest, "&gt;"):
		b.WriteString("\\>")
		return 4
	}
	n := 1
	if strings.HasPrefix(rest, "&amp;") {
		n = 5
	}
	if entityLike(s[i+n:]) {
		b.WriteByte('\\')
	}
	b.WriteByte('&')
	return n
}

// entityLike reports whether s starts like the rest of an HTML entity:
// optional #, 1-32 letters or digits, then ';'.
func entityLike(s string) bool {
	s = strings.TrimPrefix(s, "#")
	n := 0
	for n < len(s) && n <= 32 && isWord(s[n]) {
		n++
	}
	return n > 0 && n <= 32 && n < len(s) && s[n] == ';'
}

// writeAngle writes the text for the '<' at s[i] and returns the bytes
// consumed (at least 1).
func writeAngle(b *strings.Builder, s string, i int, m mode, depth int) int {
	rest := s[i+1:]
	window := rest[:min(len(rest), maxSpanBytes)]
	j := strings.IndexByte(window, '>')
	if j < 0 || strings.ContainsAny(window[:j], "\n<") {
		b.WriteString("\\<")
		return 1
	}
	inner := window[:j]
	consumed := 1 + j + 1
	switch {
	case inner == "":
		b.WriteString("\\<\\>")
	case inner[0] == '@' || inner[0] == '#':
		// User and channel references: stripped.
	case inner[0] == '!':
		// <!date^1700000000^{date}|fallback> keeps its fallback text; <!channel>,
		// <!here>, <!everyone> and <!subteam^...> go.
		if strings.HasPrefix(inner, "!date") {
			if k := strings.LastIndexByte(inner, '|'); k >= 0 {
				b.WriteString(convert(decodeEntities(inner[k+1:]), modeLabel, depth+1))
			}
		}
	default:
		writeLink(b, inner, m, depth)
	}
	return consumed
}

// writeLink writes the <url|label> token whose inside is inner.
func writeLink(b *strings.Builder, inner string, m mode, depth int) {
	target, label, hasLabel := strings.Cut(inner, "|")
	target = decodeEntities(target)
	dest, ok := safeURL(target, true)
	if !m.links || depth >= maxDepth {
		ok = false
	}
	switch {
	case ok:
		text := label
		if !hasLabel || strings.TrimSpace(label) == "" {
			text = target
		}
		b.WriteByte('[')
		b.WriteString(convert(decodeEntities(strings.TrimSpace(text)), modeLabel, depth+1))
		b.WriteString("](<")
		b.WriteString(dest)
		b.WriteString(">)")
	case hasScheme(target):
		// A scheme that is not allowed (javascript:, data:, file:...): show the
		// label, or the text of the target, never a link.
		text := label
		if !hasLabel || strings.TrimSpace(label) == "" {
			text = target
		}
		b.WriteString(convert(decodeEntities(strings.TrimSpace(text)), modeLabel, depth+1))
	default:
		// Not a link at all (<div>, <b>): keep it as literal text.
		b.WriteString("\\<")
		b.WriteString(convert(decodeEntities(inner), modeLabel, depth+1))
		b.WriteString("\\>")
	}
}

// hasScheme reports whether s starts with a URL scheme and a colon.
func hasScheme(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ':':
			return i > 0
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return false
}

// safeURL returns u as a Markdown link destination (to be written inside
// <...>) when it is an absolute http or https URL, or mailto when mail is set.
// Blanks, control characters and angle brackets are refused; a backslash is
// percent-encoded.
func safeURL(u string, mail bool) (string, bool) {
	if u == "" || len(u) > maxURLBytes || !utf8.ValidString(u) {
		return "", false
	}
	for _, r := range u {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '<' || r == '>' {
			return "", false
		}
	}
	lower := strings.ToLower(u)
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		parsed, err := url.Parse(u)
		if err != nil || parsed.Host == "" {
			return "", false
		}
	case mail && strings.HasPrefix(lower, "mailto:") && len(u) > len("mailto:"):
	default:
		return "", false
	}
	u = strings.ReplaceAll(u, `\`, "%5C")
	// Markdown decodes entities in a destination, so an ampersand that would
	// start one is escaped.
	var b strings.Builder
	for i := 0; i < len(u); i++ {
		if u[i] == '&' && entityLike(u[i+1:]) {
			b.WriteByte('\\')
		}
		b.WriteByte(u[i])
	}
	return b.String(), true
}

// writeSpan writes the bold, italic or strike span that starts at s[i] and
// returns the bytes consumed, or 0 when there is none. Like Slack, a marker
// opens a span only at a word boundary, hugging the text, and closes at the
// next marker on the same line that hugs the text from the other side. A
// doubled * or ~ (**bold**, ~~strike~~, as senders write Markdown) is a span
// too.
func writeSpan(b *strings.Builder, s string, i int, m mode, depth int) int {
	c := s[i]
	if i > 0 && isWord(s[i-1]) {
		return 0
	}
	w := 1
	if c != '_' && i+1 < len(s) && s[i+1] == c {
		w = 2
	}
	if i+w >= len(s) || isSpace(s[i+w]) || s[i+w] == c {
		return 0
	}
	end := min(len(s), i+w+maxSpanBytes)
	for j := i + w + 1; j+w <= len(s) && j < end; j++ {
		if s[j] == '\n' {
			return 0
		}
		if s[j] != c || (w == 2 && s[j+1] != c) || isSpace(s[j-1]) || (j+w < len(s) && isWord(s[j+w])) {
			continue
		}
		inner := convert(s[i+w:j], m, depth+1)
		if strings.TrimSpace(inner) == "" {
			return 0
		}
		switch c {
		case '*':
			b.WriteString("**" + inner + "**")
		case '_':
			b.WriteString("*" + inner + "*")
		default:
			b.WriteString("~~" + inner + "~~")
		}
		return j + w - i
	}
	return 0
}

func isWord(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }
