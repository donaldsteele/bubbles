package push

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/pkg/notifyrules"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Delivery parameters (plan pipeline step 7).
const (
	// TTLSeconds is how long a push service holds an undelivered message.
	TTLSeconds = 3600
	// MaxExcerptChars is the body excerpt length of the "full" payload.
	MaxExcerptChars = 120
)

// Notification is what the dispatcher hands to the sender for one recipient.
// Body is the plain-text message text; it reaches the push payload only when
// the admin chose push_payload "full".
type Notification struct {
	ChannelID   string
	ChannelName string
	ChannelKind string
	MessageID   string
	SenderName  string
	Body        string
	// Critical marks a critical alert (push urgency "high").
	Critical bool
}

// Title is the notification title: "<sender> in #<channel>", or just
// "<sender>" for direct messages.
func Title(n Notification) string {
	sender := clean(n.SenderName)
	if sender == "" {
		sender = "Someone"
	}
	if n.ChannelKind == notifyrules.ChannelDM || n.ChannelKind == notifyrules.ChannelGroupDM || n.ChannelName == "" {
		return sender
	}
	return sender + " in #" + clean(n.ChannelName)
}

// BuildPayload renders the JSON a service worker receives. mode is a
// settings.Push* value: the minimal payload carries only the title and the
// ids, never any message text; the full one adds an excerpt of at most
// MaxExcerptChars characters.
func BuildPayload(n Notification, mode string) ([]byte, error) {
	p := protocol.PushPayload{Title: Title(n), ChannelID: n.ChannelID, MessageID: n.MessageID}
	if mode == settings.PushFull {
		p.Body = Excerpt(n.Body)
	}
	return json.Marshal(p)
}

// Excerpt collapses all white space in s to single spaces, drops control
// characters and cuts the result to MaxExcerptChars characters, ending in an
// ellipsis when it was cut.
func Excerpt(s string) string {
	s = clean(s)
	r := []rune(s)
	if len(r) <= MaxExcerptChars {
		return s
	}
	return strings.TrimSpace(string(r[:MaxExcerptChars-1])) + "…"
}

// clean collapses white space and removes control characters.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
