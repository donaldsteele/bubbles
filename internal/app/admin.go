package app

import "github.com/donaldsteele/bubbles/internal/httpapi"

// adminHost lets the admin endpoints read the process state of the server.
type adminHost struct{ app *App }

// Host reports the start time, open sockets, URLs and certificate state.
func (h adminHost) Host() httpapi.HostStatus {
	a := h.app
	return httpapi.HostStatus{
		StartedAt:      a.startedAt,
		Connections:    a.ws.Conns(),
		HTTPSURLs:      a.URLs(),
		OnboardingURLs: a.OnboardingURLs(),
		TLS:            a.Certs().Status(),
	}
}

// ReissueTLS re-issues the leaf certificate (hot-swapped, no restart).
func (h adminHost) ReissueTLS() error { return h.app.Certs().Reissue() }
