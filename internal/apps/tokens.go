package apps

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

const (
	// PrefixLen is the number of leading token characters stored (and shown)
	// to identify a token: "bub_" and four characters of the secret.
	PrefixLen = 8
	// maxTokenLen bounds what the authenticator hashes.
	maxTokenLen = 128
	// LastUsedInterval throttles last_used_at writes per token.
	LastUsedInterval = time.Minute
)

// IssueTokenInput are the inputs of IssueToken.
type IssueTokenInput struct {
	Scopes []string
	// ChannelIDs restricts the token to these channels; nil leaves it
	// unrestricted (the channels the bot is a member of).
	ChannelIDs []string
	// ExpiresAt is optional; it must lie in the future.
	ExpiresAt *time.Time
}

// IssuedToken is a new token: Secret is shown once and never stored.
type IssuedToken struct {
	Secret string
	Info   protocol.AppToken
}

// IssueToken creates a token for the app. Only the SHA-256 of the secret and
// its 8-character prefix are stored.
func (s *Service) IssueToken(ctx context.Context, actor audit.Actor, appID string, in IssueTokenInput) (IssuedToken, error) {
	return run(ctx, s, func(tx *store.Tx) (IssuedToken, error) { return s.IssueTokenTx(tx, actor, appID, in) })
}

// IssueTokenTx is IssueToken inside the caller's transaction.
func (s *Service) IssueTokenTx(tx *store.Tx, actor audit.Actor, appID string, in IssueTokenInput) (IssuedToken, error) {
	ctx := tx.Context()
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM apps WHERE id = ?`, appID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IssuedToken{}, notFound()
		}
		return IssuedToken{}, fmt.Errorf("load app: %w", err)
	}
	scopes, err := NormalizeScopes("scopes", in.Scopes)
	if err != nil {
		return IssuedToken{}, err
	}
	if len(scopes) == 0 {
		return IssuedToken{}, invalid("scopes", "at least one scope is required")
	}
	channels, err := s.checkChannels(tx, in.ChannelIDs)
	if err != nil {
		return IssuedToken{}, err
	}
	var expires *int64
	if in.ExpiresAt != nil {
		ms := in.ExpiresAt.UnixMilli()
		if ms <= s.now().UnixMilli() {
			return IssuedToken{}, invalid("expires_at", "expires_at must be in the future")
		}
		expires = &ms
	}
	secret, tok, err := insertToken(tx, appID, scopes, channels, expires)
	if err != nil {
		return IssuedToken{}, err
	}
	detail := map[string]any{
		"app_id": appID, "prefix": tok.Prefix, "scopes": scopes, "channel_ids": channels, "expires_at": tok.ExpiresAt,
	}
	if _, err := audit.Write(tx, actor.Entry(ActionTokenCreate, "app_token", tok.ID, detail)); err != nil {
		return IssuedToken{}, err
	}
	return IssuedToken{Secret: secret, Info: tok}, nil
}

// checkChannels verifies that every allowlisted channel exists. A nil list
// stays nil (no allowlist); an empty one is a valid allowlist of nothing.
func (s *Service) checkChannels(tx *store.Tx, ids []string) ([]string, error) {
	if ids == nil {
		return nil, nil
	}
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var one int
		err := tx.QueryRowContext(tx.Context(), `SELECT 1 FROM channels WHERE id = ?`, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, invalid("channel_ids", "unknown channel")
		}
		if err != nil {
			return nil, fmt.Errorf("check channel: %w", err)
		}
		out = append(out, id)
	}
	return out, nil
}

// insertToken stores a new token row and returns the secret.
func insertToken(tx *store.Tx, appID string, scopes, channels []string, expires *int64) (string, protocol.AppToken, error) {
	raw, err := auth.NewToken()
	if err != nil {
		return "", protocol.AppToken{}, err
	}
	secret := protocol.AppTokenPrefix + raw
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return "", protocol.AppToken{}, fmt.Errorf("encode scopes: %w", err)
	}
	var channelsJSON any
	if channels != nil {
		b, err := json.Marshal(channels)
		if err != nil {
			return "", protocol.AppToken{}, fmt.Errorf("encode channel ids: %w", err)
		}
		channelsJSON = string(b)
	}
	id, now := tx.NewID(), tx.Now()
	var exp any
	if expires != nil {
		exp = *expires
	}
	_, err = tx.Exec(`INSERT INTO app_tokens (id, app_id, token_hash, prefix, scopes, channel_ids, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, appID, auth.HashToken(secret), secret[:PrefixLen], string(scopesJSON), channelsJSON, now, exp)
	if err != nil {
		return "", protocol.AppToken{}, fmt.Errorf("insert token: %w", err)
	}
	tok := protocol.AppToken{
		ID: id, AppID: appID, Prefix: secret[:PrefixLen], Scopes: scopes, ChannelIDs: channels,
		CreatedAt: protocol.FormatTime(now),
	}
	if expires != nil {
		tok.ExpiresAt = msPtr(*expires)
	}
	return secret, tok, nil
}

// appendBearerClose appends a session.revoked event for the bot account botID
// inside tx. The realtime hub reads its token_id or app_id and closes the
// matching live connections (4009). Being committed with the change it
// reports, it cannot be lost or sent before the change is durable.
func appendBearerClose(tx *store.Tx, botID string, payload map[string]any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode session.revoked: %w", err)
	}
	if _, err := events.Append(tx, events.Event{Type: events.TypeSessionRevoked, UserID: botID, Payload: b}); err != nil {
		return fmt.Errorf("append session.revoked: %w", err)
	}
	return nil
}

// RevokeToken revokes one token of the app. Revoking a revoked token does
// nothing; a token of another app is ErrNotFound. The token's live
// connections are closed when the change commits.
func (s *Service) RevokeToken(ctx context.Context, actor audit.Actor, appID, tokenID string) error {
	_, err := run(ctx, s, func(tx *store.Tx) (struct{}, error) {
		var revoked sql.NullInt64
		var prefix, botID string
		err := tx.QueryRowContext(tx.Context(), `SELECT t.prefix, t.revoked_at, a.user_id FROM app_tokens t JOIN apps a ON a.id = t.app_id
			WHERE t.id = ? AND t.app_id = ?`, tokenID, appID).
			Scan(&prefix, &revoked, &botID)
		if errors.Is(err, sql.ErrNoRows) {
			return struct{}{}, notFound()
		}
		if err != nil {
			return struct{}{}, fmt.Errorf("load token: %w", err)
		}
		if revoked.Valid {
			return struct{}{}, nil
		}
		if _, err := tx.Exec(`UPDATE app_tokens SET revoked_at = ? WHERE id = ?`, tx.Now(), tokenID); err != nil {
			return struct{}{}, fmt.Errorf("revoke token: %w", err)
		}
		if err := appendBearerClose(tx, botID, map[string]any{"token_id": tokenID, "app_id": appID}); err != nil {
			return struct{}{}, err
		}
		_, err = audit.Write(tx, actor.Entry(ActionTokenRevoke, "app_token", tokenID, map[string]string{"app_id": appID, "prefix": prefix}))
		return struct{}{}, err
	})
	if err == nil {
		s.forget(tokenID)
	}
	return err
}

const tokenSelect = `SELECT t.id, t.app_id, t.prefix, t.scopes, t.channel_ids, t.created_at, t.expires_at, t.last_used_at, t.revoked_at
	FROM app_tokens t`

func scanToken(sc scanner) (protocol.AppToken, error) {
	var (
		t                          protocol.AppToken
		scopes                     string
		channels                   sql.NullString
		created                    int64
		expires, lastUsed, revoked sql.NullInt64
	)
	if err := sc.Scan(&t.ID, &t.AppID, &t.Prefix, &scopes, &channels, &created, &expires, &lastUsed, &revoked); err != nil {
		return t, err
	}
	var err error
	if t.Scopes, err = decodeStrings(scopes); err != nil {
		return t, fmt.Errorf("token scopes: %w", err)
	}
	if t.Scopes == nil {
		t.Scopes = []string{}
	}
	if channels.Valid {
		if t.ChannelIDs, err = decodeStrings(channels.String); err != nil {
			return t, fmt.Errorf("token channel ids: %w", err)
		}
		if t.ChannelIDs == nil {
			t.ChannelIDs = []string{}
		}
	}
	t.CreatedAt = protocol.FormatTime(created)
	t.ExpiresAt, t.LastUsedAt, t.RevokedAt = nullMs(expires), nullMs(lastUsed), nullMs(revoked)
	return t, nil
}

// Self returns the app and token behind a bearer principal. The app's token
// list is left empty.
func (s *Service) Self(ctx context.Context, appID, tokenID string) (protocol.AppSelf, error) {
	db := s.db.Read()
	app, err := scanApp(db.QueryRowContext(ctx, appSelect+` WHERE a.id = ?`, appID))
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.AppSelf{}, notFound()
	}
	if err != nil {
		return protocol.AppSelf{}, fmt.Errorf("load app: %w", err)
	}
	tok, err := scanToken(db.QueryRowContext(ctx, tokenSelect+` WHERE t.id = ? AND t.app_id = ?`, tokenID, appID))
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.AppSelf{}, notFound()
	}
	if err != nil {
		return protocol.AppSelf{}, fmt.Errorf("load token: %w", err)
	}
	return protocol.AppSelf{App: app, Token: tok}, nil
}

// Limiter is the per-token request limit (10 per second, burst 30), keyed by
// auth.TokenGrant.ID. The REST layer applies it to every bearer request.
func (s *Service) Limiter() *auth.BucketLimiter { return s.tokens }

// Per-token request limit.
const (
	TokenRatePerSecond = 10
	TokenBurst         = 30
)

// Authenticator returns the auth.Authenticator for `Authorization: Bearer
// bub_...` headers. It resolves the token by the SHA-256 of the secret and
// answers (nil, nil), which the REST layer turns into a generic 401, for
// anything that is not a live token: unknown, revoked, expired, an app that
// is disabled or a bot account that is deactivated. Only a database failure
// is an error.
func (s *Service) Authenticator() auth.Authenticator { return bearer{s} }

type bearer struct{ s *Service }

func (b bearer) Authenticate(r *http.Request) (*auth.Principal, error) {
	token, ok := bearerToken(r)
	if !ok {
		return nil, nil
	}
	return b.s.authenticate(r.Context(), token)
}

// bearerToken extracts a well-formed app token from the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	tok := strings.TrimSpace(h[7:])
	if !strings.HasPrefix(tok, protocol.AppTokenPrefix) || len(tok) > maxTokenLen {
		return "", false
	}
	return tok, true
}

// authenticate looks the token up by its hash. The hash is compared again in
// constant time, so the decision never depends on a timing side channel of
// the database index.
func (s *Service) authenticate(ctx context.Context, token string) (*auth.Principal, error) {
	hash := auth.HashToken(token)
	var (
		tokenID, appID, userID, scopes, username, display, role, kind string
		storedHash                                                    []byte
		channels                                                      sql.NullString
		expires, revoked, lastUsed, appDisabled, userDeact            sql.NullInt64
		userCreated                                                   int64
	)
	err := s.db.Read().QueryRowContext(ctx, `SELECT t.id, t.app_id, t.token_hash, t.scopes, t.channel_ids, t.expires_at, t.revoked_at,
			t.last_used_at, a.disabled_at, a.user_id, u.username, u.display_name, u.role, u.kind, u.created_at, u.deactivated_at
		FROM app_tokens t JOIN apps a ON a.id = t.app_id JOIN users u ON u.id = a.user_id
		WHERE t.token_hash = ?`, hash).
		Scan(&tokenID, &appID, &storedHash, &scopes, &channels, &expires, &revoked, &lastUsed, &appDisabled,
			&userID, &username, &display, &role, &kind, &userCreated, &userDeact)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load app token: %w", err)
	}
	now := s.now().UnixMilli()
	if subtle.ConstantTimeCompare(storedHash, hash) != 1 || revoked.Valid || appDisabled.Valid || userDeact.Valid ||
		kind != protocol.KindApp || (expires.Valid && expires.Int64 <= now) {
		return nil, nil
	}
	grant := &auth.TokenGrant{ID: tokenID, AppID: appID}
	if expires.Valid {
		grant.ExpiresAt = expires.Int64
	}
	if grant.Scopes, err = decodeStrings(scopes); err != nil {
		return nil, fmt.Errorf("token scopes: %w", err)
	}
	if channels.Valid {
		if grant.ChannelIDs, err = decodeStrings(channels.String); err != nil {
			return nil, fmt.Errorf("token channel ids: %w", err)
		}
		if grant.ChannelIDs == nil {
			grant.ChannelIDs = []string{}
		}
	}
	s.touch(ctx, tokenID, lastUsed, now)
	return &auth.Principal{
		User: &auth.User{
			ID: userID, Username: username, DisplayName: display, Kind: kind, Role: auth.Role(role),
			Prefs: json.RawMessage("{}"), CreatedAt: userCreated,
		},
		Via:   auth.ViaBearer,
		Token: grant,
	}, nil
}

// touch records last_used_at at most once per LastUsedInterval per token. A
// failure is logged, not returned: the request itself is authenticated.
func (s *Service) touch(ctx context.Context, tokenID string, lastUsed sql.NullInt64, now int64) {
	interval := LastUsedInterval.Milliseconds()
	if lastUsed.Valid && now-lastUsed.Int64 < interval {
		return
	}
	s.mu.Lock()
	if last, ok := s.touched[tokenID]; ok && now-last < interval {
		s.mu.Unlock()
		return
	}
	s.touched[tokenID] = now
	s.mu.Unlock()
	err := s.db.Do(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE app_tokens SET last_used_at = ? WHERE id = ?`, now, tokenID)
		return err
	})
	if err != nil {
		s.logger.Warn("update token last_used", "err", err)
		s.forget(tokenID)
	}
}

func (s *Service) forget(tokenID string) {
	s.mu.Lock()
	delete(s.touched, tokenID)
	s.mu.Unlock()
}

// Endpoint says how clients reach this server; it fills the connection
// string of a new token.
type Endpoint struct {
	// Host is the address clients should use (the first LAN address).
	Host string
	// Port is the HTTPS port.
	Port int
	// Fingerprint is the SHA-256 fingerprint of the CA certificate in any
	// common notation (hex, optionally with spaces or colons).
	Fingerprint string
}

// ConnectionString returns bubbles://<host>:<port>/?token=...&fp=sha256:<hex>.
// An endpoint without a fingerprint leaves fp out.
func (e Endpoint) ConnectionString(token string) string {
	var b strings.Builder
	b.WriteString("bubbles://")
	b.WriteString(net.JoinHostPort(e.Host, strconv.Itoa(e.Port)))
	b.WriteString("/?token=")
	b.WriteString(token)
	if fp := NormalizeFingerprint(e.Fingerprint); fp != "" {
		b.WriteString("&fp=sha256:")
		b.WriteString(fp)
	}
	return b.String()
}

// NormalizeFingerprint returns fp as lower-case hex without separators.
func NormalizeFingerprint(fp string) string {
	fp = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fp)), "sha256:")
	return strings.NewReplacer(" ", "", ":", "").Replace(fp)
}
