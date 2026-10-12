package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// How a request proved who it is.
const (
	ViaCookie = "cookie"
	// ViaBearer is an app token in the Authorization header.
	ViaBearer = "bearer"
)

// Principal is an authenticated request identity.
type Principal struct {
	User *User
	// Session is the cookie session; nil for bearer-token principals.
	Session *Session
	// Via is ViaCookie or ViaBearer.
	Via string
	// Token is what an app token grants; nil for cookie principals.
	Token *TokenGrant
	// Device is what a tray device token grants; nil unless Via is ViaDevice.
	Device *DeviceGrant
}

// TokenGrant is the authority of the app token behind a ViaBearer principal.
type TokenGrant struct {
	// ID is the token id (not the secret).
	ID    string
	AppID string
	// Scopes are the scopes the token carries.
	Scopes []string
	// ChannelIDs is the token's channel allowlist; nil means every channel
	// the app's bot is a member of.
	ChannelIDs []string
	// ExpiresAt is the token's expiry in unix ms; 0 means it never expires.
	ExpiresAt int64
}

type ctxKey int

const (
	principalKey ctxKey = iota
	authErrKey
)

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFrom returns the request's principal, or nil when anonymous.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey).(*Principal)
	return p
}

// UserFrom returns the authenticated user of the request, or nil.
func UserFrom(ctx context.Context) *User {
	if p := PrincipalFrom(ctx); p != nil {
		return p.User
	}
	return nil
}

// ErrorFrom returns the infrastructure error (database failure) that kept
// an authenticator from deciding, or nil. Handlers answer 503/500 for it
// rather than treating the request as merely anonymous.
func ErrorFrom(ctx context.Context) error {
	err, _ := ctx.Value(authErrKey).(error)
	return err
}

// Authenticator resolves the credentials of a request. It returns
// (nil, nil) when the request carries none of its credentials or they are
// not valid, and a non-nil error only for infrastructure failures.
//
// The app token authenticator (internal/apps) returns a Principal with
// Via == ViaBearer and is passed to Middleware next to CookieAuthenticator.
type Authenticator interface {
	Authenticate(r *http.Request) (*Principal, error)
}

// cookieAuthenticator authenticates by the session cookie.
type cookieAuthenticator struct{ s *Service }

// CookieAuthenticator returns the Authenticator for the session cookie.
// A request carrying an Authorization header is left to the bearer
// authenticators: its cookie, if any, is ignored.
func (s *Service) CookieAuthenticator() Authenticator { return cookieAuthenticator{s} }

func (c cookieAuthenticator) Authenticate(r *http.Request) (*Principal, error) {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ") {
		return nil, nil
	}
	ck, err := r.Cookie(CookieName)
	if err != nil {
		return nil, nil
	}
	p, err := c.s.Authenticate(r.Context(), ck.Value)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return p, err
}

// Middleware loads the principal into the request context: the first
// authenticator that recognizes the request wins. It never rejects a
// request; anonymous requests simply carry no principal (handlers decide
// whether that is acceptable, see UserFrom and ErrorFrom).
func Middleware(auths ...Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			for _, a := range auths {
				p, err := a.Authenticate(r)
				if err != nil {
					ctx = context.WithValue(ctx, authErrKey, err)
					break
				}
				if p != nil {
					ctx = WithPrincipal(ctx, p)
					break
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
