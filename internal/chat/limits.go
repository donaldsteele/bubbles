package chat

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits enforced by the chat service. Most mirror CHECK constraints of the
// schema so a violation is a clear validation error instead of a constraint
// failure.
const (
	MaxBodyChars           = 8000
	MaxFileIDs             = 10
	MaxNonceLen            = 64
	MaxOverrideNameChars   = 80
	MaxChannelNameLen      = 64
	MaxTopicChars          = 250
	MaxDescriptionChars    = 1000
	MaxGroupDMParticipants = 9
	MaxInitialMembers      = 200
	MaxReactionsPerMessage = 50
	MaxEmojiBytes          = 64
	MaxMentionedUsers      = 50
	DefaultPageLimit       = 50
	MaxPageLimit           = 100
)

// chars counts characters the way SQLite's length() does for TEXT.
func chars(s string) int { return utf8.RuneCountInString(s) }

// validText reports whether s is valid UTF-8 without NUL characters.
func validText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// NormalizeChannelName lowercases name, trims blanks and one leading "#" and
// reports why the result is not a valid channel name ("" when valid).
func NormalizeChannelName(name string) (string, string) {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimPrefix(n, "#")
	if n == "" {
		return n, "name is required"
	}
	if len(n) > MaxChannelNameLen {
		return n, "name must be at most 64 characters"
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return n, "name may contain only lowercase letters, digits and hyphens"
		}
	}
	return n, ""
}

// validateEmoji reports why emoji is not acceptable ("" when it is): 1-64
// bytes, valid UTF-8, no whitespace or control characters.
func validateEmoji(emoji string) string {
	switch {
	case emoji == "":
		return "emoji is required"
	case len(emoji) > MaxEmojiBytes:
		return "emoji must be at most 64 bytes"
	case !utf8.ValidString(emoji):
		return "emoji must be valid UTF-8"
	}
	for _, r := range emoji {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "emoji must not contain whitespace"
		}
	}
	return ""
}

// cleanBody trims leading blank lines and trailing whitespace; it keeps the
// indentation of the first text line so an indented code block survives.
func cleanBody(body string) string {
	for {
		i := strings.IndexByte(body, '\n')
		if i < 0 || strings.TrimSpace(body[:i]) != "" {
			break
		}
		body = body[i+1:]
	}
	return strings.TrimRightFunc(body, unicode.IsSpace)
}

// clampLimit applies the default and maximum page size.
func clampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultPageLimit
	case n > MaxPageLimit:
		return MaxPageLimit
	}
	return n
}
