package httpapi

import (
	"net/http"
	"path"
	"strings"
)

// spaHandler serves the built single-page app from files (an http.FileServer
// over the Vite output). Client-side routes, i.e. paths whose last segment
// has no file extension, get index.html so deep links and reloads work;
// paths with an extension are files and 404 when missing.
func spaHandler(files http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := r.URL.Path
		switch {
		case p == "/sw.js" || p == "/manifest.webmanifest":
			// The worker script and manifest are fetched by URL without a content
			// hash, so browsers must revalidate them on every load for updates.
			w.Header().Set("Cache-Control", "no-cache")
			if p == "/sw.js" {
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			} else {
				w.Header().Set("Content-Type", "application/manifest+json")
			}
		case strings.HasPrefix(p, "/assets/") && strings.Contains(path.Base(p), "."):
			// Vite file names carry a content hash.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case isClientRoute(p):
			w.Header().Set("Cache-Control", "no-cache")
			if p != "/" {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/"
				r2.URL.RawPath = ""
				r = r2
			}
		}
		files.ServeHTTP(w, r)
	})
}

// isClientRoute reports whether p names an SPA route rather than a file.
func isClientRoute(p string) bool {
	return p == "/" || !strings.Contains(path.Base(p), ".")
}
