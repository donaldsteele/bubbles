package hooks

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/chat"
)

// iconEmoji is the only icon a webhook may show: a :shortcode: that becomes
// a prefix of the message. icon_url is ignored, so a sender cannot make the
// server or a client fetch an image.
var iconEmoji = regexp.MustCompile(`^:[A-Za-z0-9_+\-]{1,64}:$`)

// translateSlack turns a Slack-style payload into Markdown: the text, the
// blocks and the (legacy) attachments, in that order. As in Slack, text is
// only a notification fallback when blocks are present.
func translateSlack(p map[string]any) string {
	var parts []string
	blocks, supported := renderBlocks(asList(p["blocks"], maxBlocks))
	if supported == 0 {
		if t := Mrkdwn(asStr(p["text"])); t != "" {
			parts = append(parts, t)
		}
	}
	if blocks != "" {
		parts = append(parts, blocks)
	}
	size := 0
	for _, a := range asList(p["attachments"], maxAttachments) {
		pre, quoted := attachment(asMap(a))
		for _, s := range []string{pre, quoted} {
			if s != "" {
				parts = append(parts, s)
				size += len(s)
			}
		}
		if size > 4*chat.MaxBodyChars {
			break
		}
	}
	body := strings.Join(parts, "\n\n")
	if icon := asStr(p["icon_emoji"]); body != "" && iconEmoji.MatchString(icon) {
		body = prefixFirst(body, icon)
	}
	return body
}

// attachment renders one legacy attachment: its pretext (above the bar) and
// the quoted block holding title, text, fields, blocks and footer.
func attachment(a map[string]any) (pretext, quoted string) {
	pretext = Mrkdwn(asStr(a["pretext"]))
	var parts []string
	if title := Plain(asStr(a["title"])); title != "" {
		if dest, ok := safeURL(asStr(a["title_link"]), false); ok {
			title = "[" + title + "](<" + dest + ">)"
		}
		parts = append(parts, "**"+title+"**")
	}
	if t := Mrkdwn(asStr(a["text"])); t != "" {
		parts = append(parts, t)
	}
	var items []string
	for _, f := range asList(a["fields"], maxListItems) {
		fm := asMap(f)
		title, value := Plain(asStr(fm["title"])), Mrkdwn(asStr(fm["value"]))
		switch {
		case title != "" && value != "":
			items = append(items, listItem("**"+title+"**: "+value))
		case title != "":
			items = append(items, listItem("**"+title+"**"))
		case value != "":
			items = append(items, listItem(value))
		}
	}
	if len(items) > 0 {
		parts = append(parts, strings.Join(items, "\n"))
	}
	if blocks, _ := renderBlocks(asList(a["blocks"], maxBlocks)); blocks != "" {
		parts = append(parts, blocks)
	}
	if f := footer(a); f != "" {
		parts = append(parts, f)
	}
	if len(parts) == 0 && pretext == "" {
		if fb := Mrkdwn(asStr(a["fallback"])); fb != "" {
			parts = append(parts, fb)
		}
	}
	if len(parts) == 0 {
		return pretext, ""
	}
	return pretext, quote(prefixFirst(strings.Join(parts, "\n\n"), colorMark(asStr(a["color"]))))
}

// footer renders the footer text and the timestamp as one italic line.
func footer(a map[string]any) string {
	var items []string
	if f := Plain(asStr(a["footer"])); f != "" {
		items = append(items, f)
	}
	if ts, err := strconv.ParseFloat(strings.TrimSpace(asStr(a["ts"])), 64); err == nil && ts >= 0 && ts < 253402300799 {
		items = append(items, time.Unix(int64(ts), 0).UTC().Format("2006-01-02 15:04 UTC"))
	}
	if len(items) == 0 {
		return ""
	}
	return "*" + strings.Join(items, " · ") + "*"
}

// colorMark maps an attachment color to a status marker: Slack's named colors
// good, warning and danger, or a hex color by its dominant channel. The title
// or text still carries the meaning; the marker only adds to it.
func colorMark(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	switch c {
	case "good":
		return "\U0001F7E2"
	case "warning":
		return "\U0001F7E1"
	case "danger":
		return "\U0001F534"
	}
	h := strings.TrimPrefix(c, "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return ""
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return ""
	}
	r, g, b := int(v>>16&0xff), int(v>>8&0xff), int(v&0xff)
	switch {
	case r >= 150 && g < 110 && b < 150:
		return "\U0001F534"
	case r >= 180 && g >= 110 && b < 110:
		return "\U0001F7E1"
	case g >= 120 && g > r && g >= b:
		return "\U0001F7E2"
	}
	return ""
}

// prefixFirst puts prefix in front of md on the same line, or on a line of
// its own when md starts with Markdown block syntax a prefix would break.
func prefixFirst(md, prefix string) string {
	if prefix == "" || md == "" {
		return md
	}
	line, _, _ := strings.Cut(md, "\n")
	if startsBlock(line) {
		return prefix + "\n\n" + md
	}
	return prefix + " " + md
}

// startsBlock reports whether a line begins with a list marker, quote, rule,
// fence, table row or heading.
func startsBlock(line string) bool {
	t := strings.TrimLeft(line, " ")
	for _, p := range []string{"- ", "+ ", "* ", ">", "#", "---", "```", "|", "==="} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(t) && (t[i] == '.' || t[i] == ')') && t[i+1] == ' '
}

// quote turns md into a Markdown blockquote.
func quote(md string) string {
	lines := strings.Split(md, "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

// fit cuts body to chat.MaxBodyChars, closing a code fence the cut left open.
func fit(body string) string {
	body = strings.TrimSpace(body)
	if utf8.RuneCountInString(body) <= chat.MaxBodyChars {
		return body
	}
	const note = "\n\n…(truncated)"
	keep := chat.MaxBodyChars - utf8.RuneCountInString(note) - len("\n```")
	cut := strings.TrimRight(string([]rune(body)[:keep]), "\\ \n\t")
	if strings.Count(cut, "```")%2 == 1 {
		cut += "\n```"
	}
	return cut + note
}
