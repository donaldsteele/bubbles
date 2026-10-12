// Package webui serves the embedded Svelte single-page app.
//
// Vite builds the app into dist/ (see web/vite.config.ts). A fresh checkout
// only contains dist/.gitkeep, so the handler falls back to a placeholder page
// and the Go build works without Node.
package webui

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var distFS embed.FS

// Built reports whether the SPA has been built into dist/.
func Built() bool {
	return built(distFS)
}

// Handler serves the SPA from dist/ when it is built and the placeholder page otherwise.
func Handler() http.Handler {
	return handler(distFS)
}

// built reports whether root contains dist/index.html as a regular file.
func built(root fs.FS) bool {
	info, err := fs.Stat(root, "dist/index.html")
	return err == nil && !info.IsDir()
}

func handler(root fs.FS) http.Handler {
	if !built(root) {
		return http.HandlerFunc(placeholder)
	}
	sub, err := fs.Sub(root, "dist")
	if err != nil {
		// Unreachable: "dist" is a valid literal path inside the embed root.
		panic("webui: " + err.Error())
	}
	return http.FileServerFS(sub)
}

func placeholder(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = io.WriteString(w, placeholderPage)
}

const placeholderPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>bubbles</title></head>
<body>
<h1>UI not built</h1>
<p>Run <code>npm --prefix web run build</code>, then rebuild the server.</p>
</body>
</html>
`
