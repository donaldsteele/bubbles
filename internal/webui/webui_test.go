package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBuilt(t *testing.T) {
	tests := []struct {
		name string
		root fs.FS
		want bool
	}{
		{
			name: "built app has index.html",
			root: fstest.MapFS{"dist/index.html": {Data: []byte("<!doctype html>")}},
			want: true,
		},
		{
			name: "fresh checkout has only .gitkeep",
			root: fstest.MapFS{"dist/.gitkeep": {}},
			want: false,
		},
		{
			name: "empty root",
			root: fstest.MapFS{},
			want: false,
		},
		{
			name: "index.html path is a directory",
			root: fstest.MapFS{"dist/index.html/child": {}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := built(tt.root); got != tt.want {
				t.Errorf("built() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandler(t *testing.T) {
	tests := []struct {
		name       string
		root       fs.FS
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "not built serves placeholder",
			root:       fstest.MapFS{"dist/.gitkeep": {}},
			path:       "/",
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "npm --prefix web run build",
		},
		{
			name:       "not built ignores request path",
			root:       fstest.MapFS{"dist/.gitkeep": {}},
			path:       "/some/deep/path",
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "UI not built",
		},
		{
			name:       "built serves index at root",
			root:       fstest.MapFS{"dist/index.html": {Data: []byte("<h1>app</h1>")}},
			path:       "/",
			wantStatus: http.StatusOK,
			wantBody:   "<h1>app</h1>",
		},
		{
			name:       "built serves static asset",
			root:       fstest.MapFS{"dist/index.html": {Data: []byte("x")}, "dist/app.js": {Data: []byte("var app = 1;")}},
			path:       "/app.js",
			wantStatus: http.StatusOK,
			wantBody:   "var app = 1;",
		},
		{
			name:       "built returns 404 for missing file",
			root:       fstest.MapFS{"dist/index.html": {Data: []byte("x")}},
			path:       "/missing.js",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler(tt.root).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body %q does not contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestPlaceholderHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	handler(fstest.MapFS{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
}
