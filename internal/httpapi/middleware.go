package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"

	"github.com/donaldsteele/bubbles/internal/auth"
)

// ContentSecurityPolicy is sent on every HTTPS response (Locked Decision 2).
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' blob: data:; media-src 'self' blob:; connect-src 'self'; worker-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// PermissionsPolicy turns off powerful features the app never uses.
const PermissionsPolicy = "camera=(), microphone=(), geolocation=(), payment=(), usb=()"

// RequestIDHeader carries the per-request id back to the client.
const RequestIDHeader = "X-Request-Id"

type ctxKey int

const requestIDKey ctxKey = iota

// RequestID returns the id the requestID middleware assigned, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// chain wraps h so that the first middleware is the outermost.
func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// requestID gives every request a random id (response header and context).
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [12]byte
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// recoverer turns a handler panic into a logged 500 "internal" error. The
// log carries the request id and stack, never the URL (paths can contain
// file names).
func recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				logger.Error("handler panic", "request_id", RequestID(r.Context()), "method", r.Method,
					"panic", v, "stack", string(debug.Stack()))
				WriteError(w, http.StatusInternalServerError, CodeInternal, "internal server error")
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders sets the CSP and the other browser hardening headers. HSTS
// is deliberately not sent: the certificate comes from a local CA that a
// device may not have installed yet, and HSTS would lock it out.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", PermissionsPolicy)
		next.ServeHTTP(w, r)
	})
}

// MaxJSONBody is the largest request body a JSON endpoint accepts.
const MaxJSONBody = 1 << 20

// decodeJSON reads the request body (at most MaxJSONBody) into dst. On any
// problem it writes the error response and returns false: 415 for a
// non-JSON Content-Type, 413 for an oversized body, 400 for an empty,
// malformed or multi-document body, and 400 with details.fields naming the
// field for a key the target type does not declare (request schemas are
// additionalProperties:false).
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		WriteError(w, http.StatusUnsupportedMediaType, CodeUnsupportedMedia, "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxJSONBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil {
		// Reject trailing data after the first JSON value.
		if _, err2 := dec.Token(); err2 != io.EOF {
			err = errors.New("trailing data")
		}
	}
	if err == nil {
		return true
	}
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		WriteError(w, http.StatusRequestEntityTooLarge, CodeTooLarge, "request body is larger than 1 MiB")
	case errors.Is(err, io.EOF):
		WriteError(w, http.StatusBadRequest, CodeBadRequest, "request body is required")
	case unknownField(err) != "":
		WriteFieldErrors(w, http.StatusBadRequest, CodeBadRequest, "one or more fields are invalid",
			map[string]string{unknownField(err): "unknown field"})
	default:
		WriteError(w, http.StatusBadRequest, CodeBadRequest, "request body is not valid JSON of the expected shape")
	}
	return false
}

// unknownField returns the name of the key encoding/json rejected under
// DisallowUnknownFields, or "" when err is some other decode error. The
// standard library exposes no typed error for this case, so the message is
// parsed: `json: unknown field "name"`.
func unknownField(err error) string {
	s, ok := strings.CutPrefix(err.Error(), "json: unknown field ")
	if !ok {
		return ""
	}
	name, uerr := strconv.Unquote(s)
	if uerr != nil {
		return ""
	}
	return name
}

// clientIP returns the peer address of r without its port, in canonical
// form. bubbles runs without a reverse proxy, so forwarding headers are
// never trusted.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap().WithZone("").String()
	}
	return host
}

// requireUser answers 401 unless the auth middleware found a principal, and
// applies the rate limit of the credential: per session (20 req/s, burst 50)
// for a cookie, per token (10 req/s, burst 30) for an app token. A nil
// limiter skips that limit.
func requireUser(sessions, tokens *auth.BucketLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := auth.ErrorFrom(r.Context()); err != nil {
			WriteError(w, http.StatusServiceUnavailable, CodeUnavailable, "authentication is temporarily unavailable")
			return
		}
		p := auth.PrincipalFrom(r.Context())
		if p == nil {
			WriteError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
			return
		}
		switch {
		case p.Token != nil && tokens != nil:
			if ok, wait := tokens.Allow(p.Token.ID); !ok {
				WriteRateLimited(w, wait)
				return
			}
		case p.Device != nil && sessions != nil:
			if ok, wait := sessions.Allow("device:" + p.Device.ID); !ok {
				WriteRateLimited(w, wait)
				return
			}
		case p.Session != nil && sessions != nil:
			if ok, wait := sessions.Allow(auth.PublicSessionID(p.Session.IDHash)); !ok {
				WriteRateLimited(w, wait)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// humansOnly answers 403 to a valid app token: the endpoint is for people
// signed in with a browser session. Anonymous requests pass (public routes
// serve them) and invalid tokens never reach here as principals.
func humansOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := auth.PrincipalFrom(r.Context()); p != nil && p.Via != auth.ViaCookie {
			WriteError(w, http.StatusForbidden, CodeForbidden, "this endpoint is not available to app tokens")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// deviceRoutes are the only routes a tray device token may call besides the
// device endpoints themselves: the bootstrap snapshot and the message reads
// needed to render a notification. The WebSocket upgrade has its own mux
// entry. Everything else is 403 for a device (deny by default).
var deviceRoutes = map[string]bool{
	"GET " + apiPrefix + "/bootstrap":              true,
	"GET " + apiPrefix + "/users/{id}":             true,
	"GET " + apiPrefix + "/channels/{id}/messages": true,
	"GET " + apiPrefix + "/messages/{id}/thread":   true,
}

// deviceGate lets a device principal through only on deviceRoutes.
func deviceGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := auth.PrincipalFrom(r.Context()); p != nil && p.Via == auth.ViaDevice && !deviceRoutes[r.Pattern] {
			WriteError(w, http.StatusForbidden, CodeForbidden, "this endpoint is not available to device tokens")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminOnly answers 403 unless the signed-in user is an admin. It runs after
// requireUser.
func adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFrom(r.Context())
		if u == nil || !u.Role.IsAdmin() {
			WriteError(w, http.StatusForbidden, CodeForbidden, "admin access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireScope is the scope check of handlers an app token may reach: it
// answers 403 and returns false when the caller is an app token without
// scope. Browser sessions are not scoped and always pass.
func requireScope(w http.ResponseWriter, r *http.Request, scope string) bool {
	p := auth.PrincipalFrom(r.Context())
	if p == nil || p.Token == nil || slices.Contains(p.Token.Scopes, scope) {
		return true
	}
	WriteError(w, http.StatusForbidden, CodeForbidden, "the token lacks the "+scope+" scope")
	return false
}
