package httpapi

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/mcpserver"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// maxMCPBody bounds one MCP request (a JSON-RPC message).
const maxMCPBody = 1 << 20

// registerMCPRoutes mounts the MCP server at /api/v1/mcp as Streamable HTTP.
//
// The endpoint is for app tokens that carry the mcp scope: browser sessions
// and tokens without the scope get 403. Each request runs through the same
// bearer authentication, per-token rate limit and (skipped for bearer) Origin
// check as the REST routes. The transport is stateless: no Mcp-Session-Id is
// issued, every POST is answered with one JSON body and the server builds a
// fresh MCP server bound to that request's token, so a session id can never
// carry one token's identity into another request. GET and DELETE answer 405 (there are
// no server-initiated messages). Tools run the chat, search and alert services
// in-process as the token's bot user, so channel allowlist and membership
// rules are the ones of the REST API.
func registerMCPRoutes(mux *http.ServeMux, d Deps, lim limits) {
	svc := mcpserver.Services{Chat: d.Chat, Search: d.Search, Alerts: d.Alerts}
	shared := mcpserver.Limits{Posts: lim.posts, Alerts: lim.alerts}
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		b := mcpserver.NewLocal(svc, actorFrom(r), auditActorFrom(r), shared)
		return mcpserver.New(b, mcpserver.Info{Version: d.Version, Logger: d.Logger})
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// The bearer token already defeats DNS rebinding; requests that reach
		// the server on loopback with the instance's host name must work.
		DisableLocalhostProtection: true,
		MaxRequestBodyBytes:        maxMCPBody,
	})
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := auth.PrincipalFrom(r.Context())
		if p == nil || p.Via != auth.ViaBearer || p.Token == nil {
			WriteError(w, http.StatusForbidden, CodeForbidden, "the MCP endpoint is for app tokens")
			return
		}
		if !slices.Contains(p.Token.Scopes, protocol.ScopeMCP) {
			WriteError(w, http.StatusForbidden, CodeForbidden, "the token lacks the mcp scope")
			return
		}
		h.ServeHTTP(&noStore{ResponseWriter: w}, r)
	})
	route := appRoute(d)
	mux.Handle("POST "+apiPrefix+"/mcp", route(guarded))
}

// noStore keeps Cache-Control: no-store on responses whatever the MCP handler
// sets (tool results are private) and turns the handler's plain-text HTTP
// errors into the API's JSON error body.
type noStore struct {
	http.ResponseWriter
	failed bool
}

func (w *noStore) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "no-store")
	if status >= 400 {
		w.failed = true
		w.Header().Set("Content-Type", "application/json")
		w.Header().Del("Content-Length")
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *noStore) Write(b []byte) (int, error) {
	if !w.failed {
		w.Header().Set("Cache-Control", "no-store")
		return w.ResponseWriter.Write(b)
	}
	msg := strings.TrimSpace(string(b))
	out, _ := json.Marshal(protocol.ErrorBody{Error: protocol.Error{Code: CodeBadRequest, Message: msg}})
	if _, err := w.ResponseWriter.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (w *noStore) Unwrap() http.ResponseWriter { return w.ResponseWriter }
