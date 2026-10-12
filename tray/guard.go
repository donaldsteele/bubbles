package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The webview loads two kinds of content. The embedded sign-in UI is served
// from the local Wails asset origin. The main window loads bubbles server
// pages from a remote origin, and the Wails runtime is injected into those
// pages too, so a script there can reach the Go bindings. The guards below
// keep the bindings and the runtime endpoints on the sign-in window.

// errNotSignInWindow rejects a bound call that did not come from the sign-in window.
var errNotSignInWindow = errors.New("sign-in is only available from the bubbles sign-in window")

// errForeignOrigin rejects a runtime request that did not come from the local UI.
var errForeignOrigin = errors.New("runtime request from a foreign origin")

// localOrigin is the origin of the embedded UI in the webview on goos: Windows
// serves it from http://wails.localhost, macOS and Linux from wails://wails.
func localOrigin(goos string) string {
	if goos == "windows" {
		return "http://wails.localhost"
	}
	return "wails://wails"
}

// windowAllowed reports whether a bound call made by the window named
// windowName may reach AuthService. Only the sign-in window may.
func windowAllowed(windowName string) bool {
	return windowName == loginWindow
}

// originAllowed reports whether a runtime request from origin may be served on
// goos. origin is the Origin header and fetchSite the Sec-Fetch-Site header.
// A same-origin request may omit Origin (GET), so an empty origin is accepted
// only when the browser says the request is same-origin. A remote page cannot
// forge Sec-Fetch-Site.
func originAllowed(goos, origin, fetchSite string) bool {
	if origin == "" {
		return fetchSite == "same-origin"
	}
	return origin == localOrigin(goos)
}

// requireSignInWindow is the window gate used by every AuthService method. It
// reads the calling window from the binding context (set by Wails for each call).
func requireSignInWindow(ctx context.Context) error {
	var name string
	if w, ok := ctx.Value(application.WindowKey).(application.Window); ok && w != nil {
		name = w.Name()
	}
	if !windowAllowed(name) {
		return errNotSignInWindow
	}
	return nil
}

// originGate refuses runtime endpoints (/wails/...) requested from a foreign
// origin, before the asset server and the runtime transport see the request.
// It is the first link of the asset middleware chain.
func originGate(goos string) application.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/wails/") &&
				!originAllowed(goos, r.Header.Get("Origin"), r.Header.Get("Sec-Fetch-Site")) {
				http.Error(w, errForeignOrigin.Error(), http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rawMessageGate logs and drops raw messages that do not come from the local UI.
// The tray uses no raw messages, so nothing is forwarded. Message bodies are
// never logged.
func rawMessageGate(logger *slog.Logger) func(application.Window, string, *application.OriginInfo) {
	return func(_ application.Window, _ string, origin *application.OriginInfo) {
		if origin == nil || !originAllowed(runtime.GOOS, origin.Origin, "") {
			logger.Debug("dropped a message from a foreign origin")
		}
	}
}
