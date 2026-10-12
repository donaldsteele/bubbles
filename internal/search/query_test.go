package search

import (
	"database/sql"
	"errors"
	"math/rand/v2"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/chat"

	_ "modernc.org/sqlite"
)

func ms(t *testing.T, loc *time.Location, date string) *int64 {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		t.Fatal(err)
	}
	v := d.UnixMilli()
	return &v
}

func TestSearchParseQuery(t *testing.T) {
	plus5 := time.FixedZone("plus5", 5*3600)
	tests := []struct {
		name string
		in   string
		loc  *time.Location
		want Query
	}{
		{"words", "hello world", time.UTC, Query{Terms: []Term{{"hello", false}, {"world", false}}}},
		{"phrase", `say "hello big world" now`, time.UTC, Query{Terms: []Term{{"say", false}, {"hello big world", true}, {"now", false}}}},
		{"unterminated phrase", `a "b c`, time.UTC, Query{Terms: []Term{{"a", false}, {"b c", true}}}},
		{"empty phrase dropped", `a "" b`, time.UTC, Query{Terms: []Term{{"a", false}, {"b", false}}}},
		{"all filters", `fix in:#general from:@alice has:file has:image has:link before:2026-03-10 after:2026-03-01`, plus5,
			Query{
				Terms: []Term{{"fix", false}}, Channels: []string{"general"}, Users: []string{"alice"},
				HasFile: true, HasImage: true, HasLink: true,
				Before: ms(t, plus5, "2026-03-10"), After: ms(t, plus5, "2026-03-01"),
			}},
		{"operators are case-insensitive", `IN:#General FROM:@Bob HAS:Image Before:2026-01-02`, time.UTC,
			Query{Channels: []string{"general"}, Users: []string{"Bob"}, HasImage: true, Before: ms(t, time.UTC, "2026-01-02")}},
		{"hash and at are optional", `in:random from:carol x`, time.UTC,
			Query{Terms: []Term{{"x", false}}, Channels: []string{"random"}, Users: []string{"carol"}}},
		{"repeated in and from are unique", `in:#a in:#b in:#a from:x from:y`, time.UTC,
			Query{Channels: []string{"a", "b"}, Users: []string{"x", "y"}}},
		{"before takes the earliest, after the latest", `before:2026-05-01 before:2026-04-01 after:2026-01-01 after:2026-02-01`, time.UTC,
			Query{Before: ms(t, time.UTC, "2026-04-01"), After: ms(t, time.UTC, "2026-02-01")}},
		{"unknown operator is a word", `foo:bar baz`, time.UTC, Query{Terms: []Term{{"foo:bar", false}, {"baz", false}}}},
		{"empty operator value is a word", `in: from:@ hello`, time.UTC,
			Query{Terms: []Term{{"in:", false}, {"from:@", false}, {"hello", false}}}},
		{"operator inside a phrase is text", `"in:#general x"`, time.UTC, Query{Terms: []Term{{"in:#general x", true}}}},
		{"punctuation-only terms are dropped", `- ( ) ^ * hello`, time.UTC, Query{Terms: []Term{{"hello", false}}}},
		{"filter only", `in:#general`, time.UTC, Query{Channels: []string{"general"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseQuery(tc.in, tc.loc)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseQuery(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSearchParseQueryDatesUseLocalMidnight(t *testing.T) {
	plus5 := time.FixedZone("plus5", 5*3600)
	got, err := ParseQuery("after:2026-03-01", plus5)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 2, 28, 19, 0, 0, 0, time.UTC).UnixMilli()
	if got.After == nil || *got.After != want {
		t.Errorf("after = %v, want %d", got.After, want)
	}
}

func TestSearchParseQueryErrors(t *testing.T) {
	tests := []struct{ name, in string }{
		{"empty", ""},
		{"blank", "   \t "},
		{"punctuation only", `- ( ) "*"`},
		{"invalid date", "before:2026-13-01"},
		{"short date", "after:2026-3-1"},
		{"date with time", "after:2026-03-01T10:00"},
		{"text instead of a date", "before:yesterday"},
		{"bad has", "has:pdf"},
		{"NUL", "a\x00b"},
		{"invalid utf-8", "a\xffb"},
		{"too long", strings.Repeat("a", MaxQueryChars+1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseQuery(tc.in, time.UTC)
			var ve *chat.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("ParseQuery(%q) error = %v, want a validation error", tc.in, err)
			}
			if _, ok := ve.Fields["q"]; !ok {
				t.Errorf("fields = %v, want a reason for q", ve.Fields)
			}
		})
	}
}

func TestSearchMatchExpression(t *testing.T) {
	tests := []struct{ in, want string }{
		{"hello", `"hello"*`},
		{"hello world", `"hello" AND "world"*`},
		{`"hello world"`, `"hello world"`},
		{`"hello world" more`, `"hello world" AND "more"*`},
		{`more "hello world"`, `"more" AND "hello world"`},
		{`say"hi there`, `"say""hi" AND "there"*`},
		{"NEAR(a b) OR x", `"NEAR(a" AND "b)" AND "OR" AND "x"*`},
		{"body_plain:secret", `"body_plain:secret"*`},
		{"-foo ^bar", `"-foo" AND "^bar"*`},
		{"in:#general", ""},
		{"in:#general café", `"café"*`},
	}
	for _, tc := range tests {
		q, err := ParseQuery(tc.in, time.UTC)
		if err != nil {
			t.Fatalf("ParseQuery(%q): %v", tc.in, err)
		}
		if got := q.Match(); got != tc.want {
			t.Errorf("Match(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// safeMatch is the only shape a MATCH expression may have: AND-ed double-quoted
// strings (quotes doubled), a star only after the last one.
var safeMatch = regexp.MustCompile(`^(?:"(?:[^"]|"")*" AND )*"(?:[^"]|"")*"\*?$`)

// ftsTable opens an in-memory database with an FTS5 table configured like
// messages_fts, for running MATCH expressions the way the service does.
func ftsTable(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`CREATE VIRTUAL TABLE t USING fts5(body_plain, tokenize = 'unicode61 remove_diacritics 2')`,
		`INSERT INTO t (body_plain) VALUES ('hello world'), ('café au lait NEAR the OR AND NOT'), ('body_plain: x (y) ^z -w *')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func checkMatch(t testing.TB, db *sql.DB, in string) {
	t.Helper()
	q, err := ParseQuery(in, time.UTC)
	if err != nil {
		var ve *chat.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("ParseQuery(%q): error %v is not a validation error", in, err)
		}
		return
	}
	if len(q.Terms) == 0 && !q.hasFilters() {
		t.Fatalf("ParseQuery(%q) accepted a query without terms or filters", in)
	}
	m := q.Match()
	if m == "" {
		return
	}
	if !safeMatch.MatchString(m) {
		t.Fatalf("Match(%q) = %q has an unsafe shape", in, m)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM t WHERE t MATCH ?`, m).Scan(&n); err != nil {
		t.Fatalf("MATCH %q (from %q): %v", m, in, err)
	}
}

func TestSearchHostileInputNeverBreaksMatch(t *testing.T) {
	db := ftsTable(t)
	defer db.Close()
	inputs := []string{
		`"`, `""`, `"""`, `*`, `**`, `-`, `--`, `NEAR`, `NEAR(a b, 2)`, `NEAR/2`, `:`, `::`, `(`, `)`, `()`, `((((`, `^`, `^a`,
		`a:b`, `body_plain:x`, `body_plain : x`, `{body_plain}: x`, `- x`, `x -y`, `x NOT y`, `x OR y`, `x AND y`, `AND`, `OR`, `NOT`,
		`"a" "b`, `"a""b"`, `a"b`, `"unterminated`, `+a`, `a+b`, `a,b`, `a;b`, `'`, `a'b`, `\`, `%`, `_`, `a%`, `[a]`, `{a}`, `a*b*`,
		`rank:x`, `rowid:1`, `t:x`, `t MATCH x`, "a\tb\nc",
		string([]rune{0xe9, 0x301}), string([]rune{0x1F4A5}), string([]rune{0x200b}), string([]rune{0xfeff}),
		strings.Repeat("a ", 128), strings.Repeat(`"`, 256), strings.Repeat("(", 255), `has:link "(x)" -y*`,
	}
	for _, in := range inputs {
		checkMatch(t, db, in)
	}
}

// TestSearchRandomInputNeverBreaksMatch throws strings built from FTS5 syntax
// fragments at the parser and the real FTS5 engine; the seed is fixed so a
// failure reproduces.
func TestSearchRandomInputNeverBreaksMatch(t *testing.T) {
	db := ftsTable(t)
	defer db.Close()
	pieces := []string{
		`"`, `""`, "*", "-", ":", "(", ")", "^", " ", " ", "NEAR", "AND", "OR", "NOT", "body_plain", "rank", "rowid", "in:", "from:@",
		"has:link", "before:2026-01-01", "a", "café", "{", "}", "[", "]", "+", ",", ";", "'", `\`, "%", "_", "\t", "x1", "0",
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 5000; i++ {
		var b strings.Builder
		for n := rng.IntN(12); n >= 0; n-- {
			b.WriteString(pieces[rng.IntN(len(pieces))])
		}
		checkMatch(t, db, b.String())
	}
}

func FuzzParseQuery(f *testing.F) {
	db := ftsTable(f)
	f.Cleanup(func() { db.Close() })
	for _, seed := range []string{
		`hello world`, `"exact phrase" more`, `in:#general from:@alice has:file`, `before:2026-03-01 after:2026-01-01 x`,
		`"`, `*`, `-`, `NEAR(a b)`, `:`, `()`, `^a`, `body_plain:x`, `a OR b NOT c`, `"a""b"`, `in:#`, `has:`, `before:`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		checkMatch(t, db, in)
	})
}
