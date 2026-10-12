package httpapi

import (
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/search"
)

// registerSearchRoutes adds GET /search. It needs a signed-in user or an app
// token with messages:read.
func registerSearchRoutes(mux *http.ServeMux, d Deps) {
	a := &api{auth: d.Auth, logger: d.Logger}
	route := appRoute(d)
	mux.Handle("GET "+apiPrefix+"/search", route(a.searchHandler(d.Search)))
}

// searchHandler answers GET /search?q=&cursor=&limit=: one page of hits in the
// channels the caller belongs to.
func (a *api) searchHandler(svc *search.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := parseSearch(w, r)
		if !ok {
			return
		}
		page, err := svc.Search(r.Context(), actorFrom(r), p)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	}
}

// parseSearch reads q (required), cursor and limit (1..search.MaxLimit) from r,
// each at most once. On any violation it answers 400 with details.fields and
// returns false. The grammar of q is checked by the search service.
func parseSearch(w http.ResponseWriter, r *http.Request) (search.Params, bool) {
	query := r.URL.Query()
	fields := map[string]string{}
	var p search.Params

	// one returns the single value of name, or "" with a reason when it is
	// repeated.
	one := func(name string) (string, bool) {
		vals := query[name]
		if len(vals) > 1 {
			fields[name] = "give " + name + " at most once"
			return "", false
		}
		if len(vals) == 0 {
			return "", false
		}
		return vals[0], true
	}

	if v, ok := one("q"); ok && v != "" {
		p.Q = v
	} else if _, repeated := fields["q"]; !repeated {
		fields["q"] = "q is required"
	}
	if v, ok := one("cursor"); ok {
		if v == "" || len(v) > search.MaxCursorChars || !utf8.ValidString(v) {
			fields["cursor"] = "is not a valid cursor"
		} else {
			p.Cursor = v
		}
	}
	if v, ok := one("limit"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > search.MaxLimit {
			fields["limit"] = "limit must be a whole number from 1 to " + strconv.Itoa(search.MaxLimit)
		} else {
			p.Limit = n
		}
	}
	if len(fields) > 0 {
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "invalid search parameters", fields)
		return search.Params{}, false
	}
	return p, true
}
