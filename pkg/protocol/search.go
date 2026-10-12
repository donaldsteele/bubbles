package protocol

// SearchResult is one hit of GET /search: the message and a short excerpt of
// its text.
//
// Snippet is an HTML fragment that is safe to render as markup: all message
// text in it is HTML-escaped (&, <, >, " and ') and the only tags are
// <mark> and </mark> around the matched terms.
type SearchResult struct {
	Message Message `json:"message"`
	Snippet string  `json:"snippet"`
}

// SearchPage is the body of GET /search, best match first. NextCursor is the
// opaque cursor of the following page, null on the last page.
type SearchPage struct {
	Items      []SearchResult `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}
