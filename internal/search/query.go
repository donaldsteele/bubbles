// Package search implements full-text message search: the query grammar, the
// FTS5 MATCH construction, membership filtering, snippets and cursor
// pagination behind GET /api/v1/search.
//
// Grammar (operators are case-insensitive, whitespace separates tokens):
//
//	words "exact phrase" in:#channel from:@user has:file|image|link
//	before:YYYY-MM-DD after:YYYY-MM-DD
//
// Words and phrases must all match (AND). The last bare word also matches as
// a prefix. Different operators narrow the result (AND); a repeated in: or
// from: widens it (OR); repeated has: operators all have to hold; repeated
// before:/after: intersect. before:D keeps messages created before the start
// of day D and after:D those created at or after it, in the server's local
// time zone. A token with an unknown operator name (or an operator with an
// empty value) is an ordinary word.
package search

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/chat"
)

// MaxQueryChars bounds the q parameter.
const MaxQueryChars = 256

// dateLayout is the format of before: and after: values.
const dateLayout = "2006-01-02"

// Term is one text term of a query.
type Term struct {
	// Text is the term as typed (without the surrounding quotes of a phrase).
	Text string
	// Phrase marks a "quoted phrase"; other terms are bare words.
	Phrase bool
}

// Query is a parsed search query.
type Query struct {
	// Terms are the words and phrases, in the order typed. Terms without a
	// single letter or digit are dropped, as the tokenizer would ignore them.
	Terms []Term
	// Channels are the in: values (lower case, without "#"); any may match.
	Channels []string
	// Users are the from: values (without "@"); any may match.
	Users []string
	// HasFile, HasImage and HasLink are the has: filters.
	HasFile, HasImage, HasLink bool
	// Before is the exclusive upper bound and After the inclusive lower bound
	// of created_at in unix milliseconds; nil means unbounded.
	Before, After *int64
}

// ParseQuery parses q. Dates are read as midnight in loc (time.Local when
// nil). The error is a *chat.ValidationError on field "q".
func ParseQuery(q string, loc *time.Location) (Query, error) {
	if loc == nil {
		loc = time.Local
	}
	switch {
	case !utf8.ValidString(q) || strings.ContainsRune(q, 0):
		return Query{}, badQuery("q must be valid text")
	case utf8.RuneCountInString(q) > MaxQueryChars:
		return Query{}, badQuery(fmt.Sprintf("q must be at most %d characters", MaxQueryChars))
	}
	var out Query
	for _, tok := range tokenize(q) {
		if tok.quoted {
			out.addTerm(tok.text, true)
			continue
		}
		key, val, ok := strings.Cut(tok.text, ":")
		val = strings.Trim(val, `"`)
		if !ok || val == "" {
			out.addTerm(tok.text, false)
			continue
		}
		var err error
		switch strings.ToLower(key) {
		case "in":
			name := strings.ToLower(strings.TrimPrefix(val, "#"))
			if name == "" {
				out.addTerm(tok.text, false)
			} else {
				out.Channels = appendUnique(out.Channels, name)
			}
		case "from":
			name := strings.TrimPrefix(val, "@")
			if name == "" {
				out.addTerm(tok.text, false)
			} else {
				out.Users = appendUnique(out.Users, name)
			}
		case "has":
			err = out.setHas(val)
		case "before":
			err = out.setBefore(val, loc)
		case "after":
			err = out.setAfter(val, loc)
		default:
			out.addTerm(tok.text, false)
		}
		if err != nil {
			return Query{}, err
		}
	}
	if len(out.Terms) == 0 && !out.hasFilters() {
		return Query{}, badQuery("q needs a search term or a filter such as in:#channel")
	}
	return out, nil
}

// Match returns the FTS5 MATCH expression for the text terms, or "" when the
// query has none (a filter-only query). Every term is one double-quoted FTS5
// string (internal quotes doubled), so no input can introduce an FTS5
// operator, column filter or syntax error; terms are AND-ed and the last term
// gets a prefix star when it is a bare word.
func (q Query) Match() string {
	var b strings.Builder
	for i, t := range q.Terms {
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteByte('"')
		b.WriteString(strings.ReplaceAll(t.Text, `"`, `""`))
		b.WriteByte('"')
		if i == len(q.Terms)-1 && !t.Phrase {
			b.WriteByte('*')
		}
	}
	return b.String()
}

func (q Query) hasFilters() bool {
	return len(q.Channels) > 0 || len(q.Users) > 0 || q.HasFile || q.HasImage || q.HasLink ||
		q.Before != nil || q.After != nil
}

func (q *Query) addTerm(text string, phrase bool) {
	if !hasTokenChar(text) {
		return
	}
	q.Terms = append(q.Terms, Term{Text: text, Phrase: phrase})
}

func (q *Query) setHas(val string) error {
	switch strings.ToLower(val) {
	case "file":
		q.HasFile = true
	case "image":
		q.HasImage = true
	case "link":
		q.HasLink = true
	default:
		return badQuery("has: must be file, image or link")
	}
	return nil
}

func (q *Query) setBefore(val string, loc *time.Location) error {
	ms, err := parseDate("before", val, loc)
	if err != nil {
		return err
	}
	if q.Before == nil || ms < *q.Before {
		q.Before = &ms
	}
	return nil
}

func (q *Query) setAfter(val string, loc *time.Location) error {
	ms, err := parseDate("after", val, loc)
	if err != nil {
		return err
	}
	if q.After == nil || ms > *q.After {
		q.After = &ms
	}
	return nil
}

func parseDate(op, val string, loc *time.Location) (int64, error) {
	t, err := time.ParseInLocation(dateLayout, val, loc)
	if err != nil {
		return 0, badQuery(op + ": must be a date like 2026-03-31")
	}
	return t.UnixMilli(), nil
}

func badQuery(reason string) error {
	return &chat.ValidationError{Fields: map[string]string{"q": reason}}
}

func appendUnique(list []string, v string) []string {
	for _, have := range list {
		if have == v {
			return list
		}
	}
	return append(list, v)
}

// hasTokenChar reports whether s holds a character the FTS5 unicode61
// tokenizer keeps (letters, numbers and private-use characters). A term made
// of nothing else would be an empty FTS5 phrase, which matches no message.
func hasTokenChar(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Co, r) {
			return true
		}
	}
	return false
}

type token struct {
	text   string
	quoted bool
}

// tokenize splits q on white space; a double-quoted run is one quoted token
// (an unterminated quote runs to the end of q). A quote inside a bare token
// is an ordinary character.
func tokenize(q string) []token {
	var out []token
	for i := 0; i < len(q); {
		r, size := utf8.DecodeRuneInString(q[i:])
		switch {
		case unicode.IsSpace(r):
			i += size
		case r == '"':
			rest := q[i+size:]
			end := strings.IndexByte(rest, '"')
			if end < 0 {
				end = len(rest)
				i = len(q)
			} else {
				i += size + end + 1
			}
			out = append(out, token{text: rest[:end], quoted: true})
		default:
			start := i
			for i < len(q) {
				r, size = utf8.DecodeRuneInString(q[i:])
				if unicode.IsSpace(r) {
					break
				}
				i += size
			}
			out = append(out, token{text: q[start:i]})
		}
	}
	return out
}
