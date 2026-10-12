package chat

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// Mentions found in a message body.
type Mentions struct {
	// Usernames are the lower-cased @username tokens in order of first
	// appearance, without @channel and @here. They are not yet checked
	// against existing users.
	Usernames []string
	Channel   bool
	Here      bool
}

// parsedBody is a body after Markdown parsing.
type parsedBody struct {
	// Plain is the searchable text: Markdown syntax removed, code kept.
	Plain    string
	Mentions Mentions
}

var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// ExtractMentions returns the mentions in a Markdown body, ignoring code
// spans, fenced and indented code blocks and raw HTML.
func ExtractMentions(body string) Mentions { return parseBody(body).Mentions }

// parseBody parses body as Markdown and returns its plain text and mentions.
// A parser panic degrades to the raw text without mentions rather than
// failing the message.
func parseBody(body string) (out parsedBody) {
	defer func() {
		if recover() != nil {
			out = parsedBody{Plain: stripC0(strings.TrimSpace(body))}
		}
	}()
	src := []byte(body)
	doc := markdown.Parser().Parse(text.NewReader(src))
	var (
		plain  strings.Builder
		code   int // depth of code spans: mentions inside are ignored
		seen   = map[string]bool{}
		result Mentions
	)
	writeSegments := func(s *text.Segments) {
		for i := 0; i < s.Len(); i++ {
			seg := s.At(i)
			plain.Write(seg.Value(src))
		}
	}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			if _, isCode := n.(*ast.CodeSpan); isCode {
				code--
			}
			if n.Type() == ast.TypeBlock && plain.Len() > 0 && !strings.HasSuffix(plain.String(), "\n") {
				plain.WriteByte('\n')
			}
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Text:
			seg := v.Segment
			plain.Write(seg.Value(src))
			if v.SoftLineBreak() || v.HardLineBreak() {
				plain.WriteByte('\n')
			}
			if code == 0 {
				scanMentions(src, v, &result, seen)
			}
		case *ast.String:
			plain.Write(v.Value)
		case *ast.CodeSpan:
			code++
		case *ast.FencedCodeBlock:
			writeSegments(v.Lines())
			return ast.WalkSkipChildren, nil
		case *ast.CodeBlock:
			writeSegments(v.Lines())
			return ast.WalkSkipChildren, nil
		case *ast.HTMLBlock:
			writeSegments(v.Lines())
			if v.HasClosure() {
				closure := v.ClosureLine
				plain.Write(closure.Value(src))
			}
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML:
			writeSegments(v.Segments)
		case *ast.AutoLink:
			plain.Write(v.URL(src))
		}
		return ast.WalkContinue, nil
	})
	return parsedBody{Plain: stripC0(strings.TrimSpace(plain.String())), Mentions: result}
}

// stripC0 removes the C0 control characters except newline and tab from
// searchable text. Search wraps highlights in 0x02 and 0x03 snippet markers,
// so a body must not be able to carry them.
func stripC0(s string) string {
	if !strings.ContainsFunc(s, isStrippedC0) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isStrippedC0(r) {
			return -1
		}
		return r
	}, s)
}

func isStrippedC0(r rune) bool { return r < 0x20 && r != 0x0a && r != 0x09 }

// scanMentions finds the @tokens whose '@' lies in the text node n. goldmark
// splits a node at emphasis delimiters, so a token is read from the raw source
// and followed into directly adjacent text nodes.
func scanMentions(src []byte, n *ast.Text, out *Mentions, seen map[string]bool) {
	seg := n.Segment
	if seg.Start < 0 || seg.Stop > len(src) {
		return
	}
	for i := seg.Start; i < seg.Stop; i++ {
		if src[i] != '@' || !mentionBoundary(src, n, i) {
			continue
		}
		name := strings.ToLower(strings.TrimRight(readToken(src, n, i+1), "."))
		switch name {
		case "channel":
			out.Channel = true
		case "here":
			out.Here = true
		default:
			if len(name) < 2 || len(name) > 32 || seen[name] || len(out.Usernames) >= MaxMentionedUsers {
				continue
			}
			seen[name] = true
			out.Usernames = append(out.Usernames, name)
		}
	}
}

// readToken reads the username characters from src[from:], which lies in n,
// continuing into the next text node while the token runs to the end of one
// and the nodes are contiguous in the source.
func readToken(src []byte, n *ast.Text, from int) string {
	var b strings.Builder
	cur, j := n, from
	for {
		stop := cur.Segment.Stop
		for j < stop && isNameByte(src[j]) {
			b.WriteByte(src[j])
			j++
		}
		if j < stop || cur.SoftLineBreak() || cur.HardLineBreak() {
			break
		}
		next, ok := cur.NextSibling().(*ast.Text)
		if !ok || next.Segment.Start != stop || next.Segment.Stop > len(src) {
			break
		}
		cur, j = next, next.Segment.Start
	}
	return b.String()
}

// mentionBoundary reports whether the '@' at src[i] starts a token: it opens
// an inline container or is not glued to a preceding word, path, address or
// escape.
func mentionBoundary(src []byte, n *ast.Text, i int) bool {
	if i == 0 || (i == n.Segment.Start && n.PreviousSibling() == nil) {
		return true
	}
	r, _ := utf8.DecodeLastRune(src[:i])
	return !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(`_.-/@\+%=`, r))
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
}
