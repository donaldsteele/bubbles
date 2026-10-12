package httpapi

import (
	"net/http"
	"time"

	"github.com/donaldsteele/bubbles/internal/auth"
)

// limits are the ingest rate limiters that more than one route draws from.
// They are built once per handler, so REST posts, POST /commands/invoke and
// MCP tool calls share the same buckets (keyed by the same ids).
type limits struct {
	// posts limits message posts per user (see messagePostsPerSecond).
	posts *auth.BucketLimiter
	// alerts limits alert ingest per app (see alertsPerSecond).
	alerts *auth.BucketLimiter
}

func newLimits(now func() time.Time) limits {
	return limits{
		posts:  auth.NewBucketLimiter(now, messagePostsPerSecond, messagePostBurst),
		alerts: auth.NewBucketLimiter(now, alertsPerSecond, alertBurst),
	}
}

// routes registers every HTTPS endpoint on mux (Go 1.22+ patterns).
func routes(mux *http.ServeMux, d Deps) {
	lim := newLimits(d.Now)
	mux.HandleFunc("GET /healthz", healthz(d.Version))
	if d.Auth != nil {
		registerAuthRoutes(mux, d)
		if d.Chat != nil {
			registerChatRoutes(mux, d, lim)
		}
		if d.Files != nil {
			registerFileRoutes(mux, d)
		}
		if d.Search != nil {
			registerSearchRoutes(mux, d)
		}
		if d.Push != nil {
			registerPushRoutes(mux, d)
		}
		if d.Apps != nil {
			registerAppRoutes(mux, d)
			registerCommandRoutes(mux, d, lim)
			if d.Alerts != nil {
				registerAlertRoutes(mux, d, lim)
				if d.Chat != nil && d.Search != nil {
					registerMCPRoutes(mux, d, lim)
				}
			}
		}
		if d.Hooks != nil {
			registerHookRoutes(mux, d)
		}
		if d.Reset != nil {
			registerResetRoutes(mux, d)
		}
		registerAdminRoutes(mux, d)
		if d.WS != nil {
			mux.Handle("GET "+apiPrefix+"/ws", auth.Middleware(authenticators(d)...)(d.WS))
		}
	}
	// Unknown API paths answer with the JSON error model, never the SPA.
	mux.HandleFunc("/api/", apiNotFound)
	mux.Handle("/", spaHandler(d.SPA))
}

// healthResponse is the body of GET /healthz.
type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

func healthz(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Version: version})
	}
}

func apiNotFound(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, http.StatusNotFound, CodeNotFound, "no such API endpoint")
}

// registerChatRoutes adds the users, channels, DMs, messages, reactions, pins,
// read-state and bootstrap endpoints. All of them need a signed-in user; the
// ones an integration needs also accept an app token (the chat service
// applies the token's scopes and channel allowlist), while channel
// management and read state are for people only.
func registerChatRoutes(mux *http.ServeMux, d Deps, lim limits) {
	a := &api{
		auth: d.Auth, chat: d.Chat, logger: d.Logger,
		posts: lim.posts,
	}
	_, human := routeKinds(d)
	shared := appRoute(d)
	route := func(method, path string, h http.HandlerFunc) {
		mux.Handle(method+" "+apiPrefix+path, shared(h))
	}
	humanRoute := func(method, path string, h http.HandlerFunc) {
		mux.Handle(method+" "+apiPrefix+path, human(h))
	}

	route("GET", "/bootstrap", a.bootstrap)

	route("GET", "/users", a.listUsers)
	route("GET", "/users/{id}", a.getUser)

	route("GET", "/channels", a.listChannels)
	humanRoute("POST", "/channels", a.createChannel)
	humanRoute("PATCH", "/channels/{id}", a.updateChannel)
	humanRoute("POST", "/channels/{id}/archive", a.archiveChannel)
	humanRoute("POST", "/channels/{id}/unarchive", a.unarchiveChannel)
	humanRoute("POST", "/channels/{id}/join", a.joinChannel)
	humanRoute("POST", "/channels/{id}/leave", a.leaveChannel)
	route("GET", "/channels/{id}/members", a.listMembers)
	humanRoute("PUT", "/channels/{id}/members/{user_id}", a.putMember)
	humanRoute("DELETE", "/channels/{id}/members/{user_id}", a.deleteMember)
	humanRoute("PATCH", "/channels/{id}/membership", a.updateMembership)
	humanRoute("POST", "/channels/{id}/read", a.markRead)
	route("POST", "/dms", a.startDM)

	route("GET", "/channels/{id}/messages", a.listMessages)
	route("POST", "/channels/{id}/messages", a.postMessage)
	route("GET", "/messages/{id}/thread", a.listThread)
	route("PATCH", "/messages/{id}", a.editMessage)
	route("DELETE", "/messages/{id}", a.deleteMessage)

	route("PUT", "/messages/{id}/reactions/{emoji}", a.putReaction)
	route("DELETE", "/messages/{id}/reactions/{emoji}", a.deleteReaction)

	route("GET", "/channels/{id}/pins", a.listPins)
	route("PUT", "/channels/{id}/pins/{message_id}", a.putPin)
	route("DELETE", "/channels/{id}/pins/{message_id}", a.deletePin)
}

// Message posts are limited per user to 10 per second with a burst of 20.
const (
	messagePostsPerSecond = 10
	messagePostBurst      = 20
)
