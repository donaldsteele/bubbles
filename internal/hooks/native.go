package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime"
	"net/url"
	"strings"
	"unicode"

	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// MaxBodyBytes is the largest request body a webhook accepts.
const MaxBodyBytes = 256 << 10

// MaxUsernameChars is the longest sender name shown with a webhook message.
const MaxUsernameChars = 64

// ErrUnsupportedMedia is returned by DecodeBody for a Content-Type other than
// application/json and application/x-www-form-urlencoded.
var ErrUnsupportedMedia = errors.New("Content-Type must be application/json or application/x-www-form-urlencoded")

// Message is a webhook payload, translated.
type Message struct {
	// Body is the Markdown text of a chat message (empty for an alert).
	Body string
	// Username is the sender name shown instead of the webhook's own; it is
	// never matched against a real user.
	Username string
	// Alert is set for a native alert payload; the alerts service posts it.
	Alert *protocol.AlertInput
}

func payloadError(reason string) error {
	return &chat.ValidationError{Fields: map[string]string{"payload": reason}}
}

// DecodeBody returns the JSON document in a webhook request body: the body
// itself for application/json, the payload= field for
// application/x-www-form-urlencoded (Slack's legacy form).
func DecodeBody(contentType string, body []byte) ([]byte, error) {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, ErrUnsupportedMedia
	}
	switch mt {
	case "application/json":
		return body, nil
	case "application/x-www-form-urlencoded":
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, payloadError("the form body is not valid")
		}
		payload := form.Get("payload")
		if payload == "" {
			return nil, payloadError("the form needs a payload field holding the JSON document")
		}
		return []byte(payload), nil
	}
	return nil, ErrUnsupportedMedia
}

// Translate turns a webhook JSON document into a Message. A document with an
// alert object is native: {"text", "username"?, "alert": {severity, title,
// text, fields, link, dedupe_key, renotify_after_s?}}. Anything else is read as
// the Slack subset (text, username, icon_emoji, attachments, blocks) and
// translated to Markdown. An empty result is a validation error.
func Translate(raw []byte) (Message, error) {
	var doc any
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&doc); err != nil {
		return Message{}, payloadError("the payload is not valid JSON")
	}
	if _, err := dec.Token(); err == nil {
		return Message{}, payloadError("the payload must be a single JSON document")
	}
	p, ok := doc.(map[string]any)
	if !ok {
		return Message{}, payloadError("the payload must be a JSON object")
	}
	if a, present := p["alert"]; present && a != nil {
		return translateNative(p, a)
	}
	body := fit(translateSlack(p))
	if body == "" {
		return Message{}, payloadError("the message has no content")
	}
	return Message{Body: body, Username: CleanUsername(asStr(p["username"]))}, nil
}

// translateNative reads the alert object of a native payload. A missing alert
// text falls back to the top-level text.
func translateNative(p map[string]any, a any) (Message, error) {
	obj, ok := a.(map[string]any)
	if !ok {
		return Message{}, payloadError("alert must be an object")
	}
	enc, err := json.Marshal(obj)
	if err != nil {
		return Message{}, payloadError("alert is not valid")
	}
	var in struct {
		Severity       string                `json:"severity"`
		Title          string                `json:"title"`
		Text           string                `json:"text"`
		Fields         []protocol.AlertField `json:"fields"`
		Link           *string               `json:"link"`
		DedupeKey      string                `json:"dedupe_key"`
		RenotifyAfterS *int                  `json:"renotify_after_s"`
	}
	if err := json.Unmarshal(enc, &in); err != nil {
		return Message{}, payloadError("alert does not have the expected shape")
	}
	if in.Text == "" {
		in.Text = asStr(p["text"])
	}
	return Message{Alert: &protocol.AlertInput{
		Severity: in.Severity, Title: in.Title, Text: in.Text, Fields: in.Fields,
		Link: in.Link, DedupeKey: in.DedupeKey, RenotifyAfterS: in.RenotifyAfterS,
	}}, nil
}

// CleanUsername makes the sender name of a webhook message safe to show: no
// control or formatting characters (bidirectional overrides included), blanks
// collapsed, at most MaxUsernameChars characters.
func CleanUsername(s string) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		switch {
		case unicode.IsSpace(r) || r == ' ' || r == ' ':
			space = b.Len() > 0
			continue
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			continue
		}
		if n >= MaxUsernameChars {
			break
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
			if n >= MaxUsernameChars {
				break
			}
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}
