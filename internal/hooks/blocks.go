package hooks

import (
	"strconv"
	"strings"
)

// Limits on what one payload may expand to; Slack's own are 50 blocks and 100
// attachments per message.
const (
	maxBlocks      = 50
	maxAttachments = 100
	maxListItems   = 50

	unsupportedBlock = "(unsupported block)"
)

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any, limit int) []any {
	l, _ := v.([]any)
	if len(l) > limit {
		l = l[:limit]
	}
	return l
}

// asStr reads a string, number or boolean the way senders use them
// interchangeably (a field value of 3, a ts of "1700000000").
func asStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

// textMD reads a Block Kit text object (or a bare string) as Markdown:
// mrkdwn is translated, plain_text is literal.
func textMD(v any) string {
	switch x := v.(type) {
	case string:
		return Mrkdwn(x)
	case map[string]any:
		if asStr(x["type"]) == "plain_text" {
			return Plain(asStr(x["text"]))
		}
		return Mrkdwn(asStr(x["text"]))
	}
	return ""
}

// textPlain reads a Block Kit text object as literal text.
func textPlain(v any) string {
	switch x := v.(type) {
	case string:
		return Plain(x)
	case map[string]any:
		return Plain(asStr(x["text"]))
	}
	return ""
}

// renderBlocks translates Block Kit blocks to Markdown. supported counts the
// blocks that produced output; a block of a kind that is not translated
// leaves an "(unsupported block)" note in its place.
func renderBlocks(blocks []any) (md string, supported int) {
	var out []string
	for _, raw := range blocks {
		if len(out) >= maxBlocks {
			break
		}
		b := asMap(raw)
		var s string
		ok := true
		switch asStr(b["type"]) {
		case "header":
			if t := textPlain(b["text"]); t != "" {
				s = "**" + t + "**"
			}
		case "section":
			s = sectionBlock(b)
		case "divider":
			s = "---"
		case "context":
			s = contextBlock(b)
		case "image":
			s, ok = imageBlock(b)
		case "actions":
			s, ok = actionsBlock(b)
		default:
			ok = false
		}
		switch {
		case !ok:
			out = append(out, unsupportedBlock)
		case s != "":
			out = append(out, s)
			supported++
		}
	}
	return strings.Join(out, "\n\n"), supported
}

// sectionBlock renders the text of a section and its fields as a list.
func sectionBlock(b map[string]any) string {
	var parts []string
	if t := textMD(b["text"]); t != "" {
		parts = append(parts, t)
	}
	var items []string
	for _, f := range asList(b["fields"], maxListItems) {
		if t := textMD(f); t != "" {
			items = append(items, listItem(t))
		}
	}
	if len(items) > 0 {
		parts = append(parts, strings.Join(items, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

// contextBlock joins the text and image elements of a context block on one
// line.
func contextBlock(b map[string]any) string {
	var items []string
	for _, e := range asList(b["elements"], maxListItems) {
		el := asMap(e)
		if asStr(el["type"]) == "image" {
			if link, ok := imageLink(el); ok {
				items = append(items, link)
			}
			continue
		}
		if t := textMD(el); t != "" {
			items = append(items, t)
		}
	}
	return strings.Join(items, " · ")
}

// imageBlock renders an image as a link: images are never loaded from
// outside the server.
func imageBlock(b map[string]any) (string, bool) {
	if link, ok := imageLink(b); ok {
		return link, true
	}
	return "", false
}

func imageLink(b map[string]any) (string, bool) {
	dest, ok := safeURL(asStr(b["image_url"]), false)
	if !ok {
		return "", false
	}
	label := Plain(asStr(b["alt_text"]))
	if label == "" {
		label = textPlain(b["title"])
	}
	if label == "" {
		label = "image"
	}
	return "[" + label + "](<" + dest + ">)", true
}

// actionsBlock keeps the buttons that open a URL as links; a block without
// any is unsupported.
func actionsBlock(b map[string]any) (string, bool) {
	var items []string
	for _, e := range asList(b["elements"], maxListItems) {
		el := asMap(e)
		if asStr(el["type"]) != "button" {
			continue
		}
		dest, ok := safeURL(asStr(el["url"]), false)
		if !ok {
			continue
		}
		label := textPlain(el["text"])
		if label == "" {
			label = "link"
		}
		items = append(items, "["+label+"](<"+dest+">)")
	}
	if len(items) == 0 {
		return "", false
	}
	return strings.Join(items, " · "), true
}

// listItem renders s as one Markdown list item, indenting its continuation
// lines.
func listItem(s string) string {
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] != "" {
			lines[i] = "  " + lines[i]
		}
	}
	return "- " + strings.Join(lines, "\n")
}
