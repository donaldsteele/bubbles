package httpapi

import (
	"net/http"
	"strconv"

	"github.com/donaldsteele/bubbles/internal/chat"
)

// maxAnchorLen bounds a before/after/around id (ids are 26-character ULIDs).
const maxAnchorLen = 64

// parsePage reads ?before=<id>|after=<id>|around=<id>&limit=<n> from r. At most
// one anchor may be given, each parameter at most once, limit is 1..100
// (default 50). On any violation it answers 400 with details.fields and
// returns false.
func parsePage(w http.ResponseWriter, r *http.Request) (chat.PageOptions, bool) {
	q := r.URL.Query()
	fields := map[string]string{}
	var o chat.PageOptions
	anchors := 0
	for _, a := range []struct {
		name string
		dst  *string
	}{{"before", &o.Before}, {"after", &o.After}, {"around", &o.Around}} {
		vals, present := q[a.name]
		if !present {
			continue
		}
		anchors++
		switch {
		case len(vals) != 1:
			fields[a.name] = "give " + a.name + " at most once"
		case vals[0] == "":
			fields[a.name] = "must not be empty"
		case len(vals[0]) > maxAnchorLen:
			fields[a.name] = "is not a valid id"
		default:
			*a.dst = vals[0]
		}
	}
	if anchors > 1 {
		for _, name := range []string{"before", "after", "around"} {
			if _, present := q[name]; present {
				fields[name] = "use only one of before, after and around"
			}
		}
	}
	if vals, present := q["limit"]; present {
		n, err := strconv.Atoi(vals[0])
		switch {
		case len(vals) != 1:
			fields["limit"] = "give limit at most once"
		case err != nil || n < 1 || n > chat.MaxPageLimit:
			fields["limit"] = "limit must be a whole number from 1 to " + strconv.Itoa(chat.MaxPageLimit)
		default:
			o.Limit = n
		}
	}
	if len(fields) > 0 {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "invalid pagination parameters", fields)
		return chat.PageOptions{}, false
	}
	return o, true
}
