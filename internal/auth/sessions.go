package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/store"
)

// Session constants (Locked Decision 2).
const (
	// CookieName carries the session token. The __Host- prefix makes
	// browsers require Secure, Path=/ and no Domain.
	CookieName = "__Host-bubbles_session"
	// AbsoluteLifetime is the longest a session lives, however active.
	AbsoluteLifetime = 90 * 24 * time.Hour
	// LastSeenInterval throttles last_seen_at / idle-expiry updates.
	LastSeenInterval = time.Minute

	// SessionWeb is a browser session; SessionHandoff a tray-to-browser one.
	SessionWeb     = "web"
	SessionHandoff = "handoff"

	maxUserAgentLen = 200
	maxLoginName    = 64
)

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewToken returns 256 random bits as lower-case base32 (52 characters). It
// is the format of session tokens, invite codes, reset codes and setup
// tokens.
func NewToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return strings.ToLower(tokenEncoding.EncodeToString(b[:])), nil
}

// HashToken returns the SHA-256 of a token; only this hash is stored.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// PublicSessionID derives the id shown to users from a session hash (its
// first 16 bytes in hex). It identifies a session in the API without
// exposing the stored hash itself.
func PublicSessionID(idHash []byte) string {
	if len(idHash) > 16 {
		idHash = idHash[:16]
	}
	return hex.EncodeToString(idHash)
}

// Session is one row of the sessions table.
type Session struct {
	// ID is PublicSessionID(IDHash).
	ID            string
	IDHash        []byte
	UserID        string
	Kind          string
	CreatedAt     int64 // unix ms
	LastSeenAt    int64
	IdleExpiresAt int64
	AbsExpiresAt  int64
	UserAgent     string
	IP            string
}

// ExpiresAt is the absolute expiry as a time.
func (s *Session) ExpiresAt() time.Time { return time.UnixMilli(s.AbsExpiresAt) }

const sessionColumns = `id_hash, user_id, kind, created_at, last_seen_at, idle_expires_at, abs_expires_at, user_agent, ip`

func (s *Session) dest() []any {
	return []any{&s.IDHash, &s.UserID, &s.Kind, &s.CreatedAt, &s.LastSeenAt, &s.IdleExpiresAt, &s.AbsExpiresAt, &s.UserAgent, &s.IP}
}

func scanSession(sc scanner) (*Session, error) {
	var s Session
	if err := sc.Scan(s.dest()...); err != nil {
		return nil, err
	}
	s.ID = PublicSessionID(s.IDHash)
	return &s, nil
}

// SessionResult is returned by every call that starts a session.
type SessionResult struct {
	// Token is the cookie value; it is never stored.
	Token   string
	User    *User
	Session Session
}

// NewCookie returns the session cookie for token, living until expires.
// Attributes are fixed: Secure, HttpOnly, SameSite=Strict, Path=/ and no
// Domain (required by the __Host- prefix).
func NewCookie(token string, expires, now time.Time) *http.Cookie {
	maxAge := int(expires.Sub(now).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	return &http.Cookie{
		Name: CookieName, Value: token, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	}
}

// ClearCookie returns a cookie that deletes the session cookie.
func ClearCookie() *http.Cookie {
	return &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	}
}

// cleanUserAgent bounds and sanitizes a User-Agent for storage.
func cleanUserAgent(ua string) string {
	ua = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, ua)
	if len(ua) > maxUserAgentLen {
		ua = ua[:maxUserAgentLen]
		for !utf8.ValidString(ua) {
			ua = ua[:len(ua)-1]
		}
	}
	return ua
}

// createSession inserts a session for userID inside tx and returns its token.
func (s *Service) createSession(tx *store.Tx, userID, kind, userAgent, ip string) (string, Session, error) {
	token, err := NewToken()
	if err != nil {
		return "", Session{}, err
	}
	now := tx.Now()
	abs := now + AbsoluteLifetime.Milliseconds()
	idle := min(now+int64(s.settings.SessionIdleDays())*24*time.Hour.Milliseconds(), abs)
	sess := Session{
		IDHash: HashToken(token), UserID: userID, Kind: kind,
		CreatedAt: now, LastSeenAt: now, IdleExpiresAt: idle, AbsExpiresAt: abs,
		UserAgent: cleanUserAgent(userAgent), IP: ip,
	}
	sess.ID = PublicSessionID(sess.IDHash)
	if _, err := tx.Exec(`INSERT INTO sessions (`+sessionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.IDHash, sess.UserID, sess.Kind, sess.CreatedAt, sess.LastSeenAt, sess.IdleExpiresAt, sess.AbsExpiresAt, sess.UserAgent, sess.IP); err != nil {
		return "", Session{}, fmt.Errorf("insert session: %w", err)
	}
	return token, sess, nil
}

// appendSessionRevoked tells the realtime layer (via the event log) that
// sessions of userID ended, so it can close their sockets. payload names
// the session ("session_id"), or "all" with an optional "except_session_id".
func appendSessionRevoked(tx *store.Tx, userID string, payload string) error {
	_, err := events.Append(tx, events.Event{Type: events.TypeSessionRevoked, UserID: userID, Payload: []byte(payload)})
	return err
}

// RevokeSessionsTx deletes every session of userID except the one whose hash
// is exceptHash (empty keeps none), inside tx, and appends a session.revoked
// event. Services that deactivate users or change passwords call it in
// their own transaction.
func RevokeSessionsTx(tx *store.Tx, userID string, exceptHash []byte) error {
	var err error
	payload := `{"all":true}`
	if len(exceptHash) == 0 {
		_, err = tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	} else {
		_, err = tx.Exec(`DELETE FROM sessions WHERE user_id = ? AND id_hash != ?`, userID, exceptHash)
		payload = `{"all":true,"except_session_id":"` + PublicSessionID(exceptHash) + `"}`
	}
	if err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return appendSessionRevoked(tx, userID, payload)
}

// Authenticate resolves a session token to its session and user. Unknown,
// expired and deactivated-user sessions return ErrNotFound. A valid session
// has last_seen_at and the idle expiry moved forward at most once per
// LastSeenInterval.
func (s *Service) Authenticate(ctx context.Context, token string) (*Principal, error) {
	if token == "" || len(token) > 128 {
		return nil, ErrNotFound
	}
	now := s.now().UnixMilli()
	row := s.db.Read().QueryRowContext(ctx, `SELECT `+userColumns("u.")+`, `+prefixed("s.", sessionColumns)+`
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id_hash = ? AND s.idle_expires_at > ? AND s.abs_expires_at > ? AND u.deactivated_at IS NULL`,
		HashToken(token), now, now)
	sess := new(Session)
	user, err := scanUser(row, sess.dest()...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	sess.ID = PublicSessionID(sess.IDHash)
	if now-sess.LastSeenAt >= LastSeenInterval.Milliseconds() {
		s.touch(ctx, sess, now)
	}
	return &Principal{User: user, Session: sess, Via: ViaCookie}, nil
}

// prefixed adds prefix to each column of a comma-separated list.
func prefixed(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = prefix + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// touch slides the idle expiry. A failure is logged, not returned: the
// request itself is authenticated.
func (s *Service) touch(ctx context.Context, sess *Session, now int64) {
	idleMs := int64(s.settings.SessionIdleDays()) * 24 * time.Hour.Milliseconds()
	idle := min(now+idleMs, sess.AbsExpiresAt)
	err := s.db.Do(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE sessions SET last_seen_at = ?, idle_expires_at = ? WHERE id_hash = ?`, now, idle, sess.IDHash)
		return err
	})
	if err != nil {
		s.logger.Warn("update session last_seen", "err", err)
		return
	}
	sess.LastSeenAt, sess.IdleExpiresAt = now, idle
}

// LoginInput is the input of Login.
type LoginInput struct {
	Username  string
	Password  string
	UserAgent string
	IP        string
	// ReplaceToken is the session token the browser presented, if any. It
	// is revoked in the same transaction (rotation on login).
	ReplaceToken string
}

// Login verifies the credentials and starts a new session. Every failure
// returns ErrInvalidCredentials, whatever the cause (unknown user, wrong
// password, deactivated, app account), after the same amount of hashing.
// Failures are rate limited per username and per IP; a blocked caller gets
// *RateLimitedError without any password check.
func (s *Service) Login(ctx context.Context, in LoginInput) (*SessionResult, error) {
	user, pwHash, newHash, err := s.verifyCredentials(ctx, in)
	if err != nil {
		return nil, err
	}
	var res SessionResult
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		var active int
		if err := tx.QueryRow(`SELECT 1 FROM users WHERE id = ? AND deactivated_at IS NULL`, user.ID).Scan(&active); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrInvalidCredentials
			}
			return err
		}
		if in.ReplaceToken != "" {
			if err := revokeByToken(tx, in.ReplaceToken); err != nil {
				return err
			}
		}
		if newHash != "" {
			if _, err := tx.Exec(`UPDATE users SET pw_hash = ? WHERE id = ? AND pw_hash = ?`, newHash, user.ID, pwHash); err != nil {
				return fmt.Errorf("rehash password: %w", err)
			}
		}
		token, sess, err := s.createSession(tx, user.ID, SessionWeb, in.UserAgent, in.IP)
		if err != nil {
			return err
		}
		res = SessionResult{Token: token, User: user, Session: sess}
		_, err = audit.Write(tx, audit.User(user.ID, in.IP).Entry(audit.ActionLoginSuccess, "session", sess.ID, nil))
		return err
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// credentials loads the user and password hash for a login; a missing user
// returns (nil, "", nil).
func (s *Service) credentials(ctx context.Context, username string) (*User, string, error) {
	var pwHash string
	u, err := scanUser(s.db.Read().QueryRowContext(ctx, `SELECT `+userColumns("")+`, COALESCE(pw_hash, '') FROM users WHERE username = ?`, username), &pwHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("load credentials: %w", err)
	}
	return u, pwHash, nil
}

// auditLoginFailure records a failed login. The attempted name is stored
// only when it looks like a username (people sometimes type a password
// into that field).
func (s *Service) auditLoginFailure(ctx context.Context, user *User, username, ip string) {
	attempted := username
	if !usernameRE.MatchString(attempted) {
		attempted = ""
	}
	actor := audit.Actor{Kind: audit.KindUser, IP: ip}
	target := ""
	if user != nil {
		actor.ID, target = user.ID, user.ID
	}
	err := s.db.Do(ctx, func(tx *store.Tx) error {
		_, err := audit.Write(tx, actor.Entry(audit.ActionLoginFailure, "user", target, map[string]string{"username": attempted}))
		return err
	})
	if err != nil {
		s.logger.Warn("audit login failure", "err", err)
	}
}

// revokeByToken deletes the session for token inside tx (no-op if unknown).
func revokeByToken(tx *store.Tx, token string) error {
	hash := HashToken(token)
	var userID string
	err := tx.QueryRow(`SELECT user_id FROM sessions WHERE id_hash = ?`, hash).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find session: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE id_hash = ?`, hash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return appendSessionRevoked(tx, userID, `{"session_id":"`+PublicSessionID(hash)+`"}`)
}

// Logout revokes the session for token. An unknown token is not an error.
func (s *Service) Logout(ctx context.Context, token, ip string) error {
	if token == "" {
		return nil
	}
	hash := HashToken(token)
	return s.db.Do(ctx, func(tx *store.Tx) error {
		var userID string
		err := tx.QueryRow(`SELECT user_id FROM sessions WHERE id_hash = ?`, hash).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("find session: %w", err)
		}
		if err := revokeByToken(tx, token); err != nil {
			return err
		}
		_, err = audit.Write(tx, audit.User(userID, ip).Entry(audit.ActionLogout, "session", PublicSessionID(hash), nil))
		return err
	})
}

// ListSessions returns userID's live sessions, most recently used first.
func (s *Service) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	now := s.now().UnixMilli()
	rows, err := s.db.Read().QueryContext(ctx, `SELECT `+sessionColumns+` FROM sessions
		WHERE user_id = ? AND idle_expires_at > ? AND abs_expires_at > ? ORDER BY last_seen_at DESC, created_at DESC`, userID, now, now)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("list sessions: %w", err)
		}
		out = append(out, *sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return out, nil
}

// RevokeSession ends one of userID's sessions by its public id; another
// user's session is ErrNotFound.
func (s *Service) RevokeSession(ctx context.Context, actor audit.Actor, userID, publicID string) error {
	publicID = strings.ToLower(publicID)
	return s.db.Do(ctx, func(tx *store.Tx) error {
		var hash []byte
		err := tx.QueryRow(`SELECT id_hash FROM sessions WHERE user_id = ? AND lower(hex(substr(id_hash, 1, 16))) = ?`, userID, publicID).Scan(&hash)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("find session: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM sessions WHERE id_hash = ?`, hash); err != nil {
			return fmt.Errorf("delete session: %w", err)
		}
		if err := appendSessionRevoked(tx, userID, `{"session_id":"`+PublicSessionID(hash)+`"}`); err != nil {
			return err
		}
		_, err = audit.Write(tx, actor.Entry(audit.ActionSessionRevoke, "session", PublicSessionID(hash), map[string]string{"user_id": userID}))
		return err
	})
}

// PurgeExpired deletes expired sessions and password-reset rows that expired
// more than a week ago, and returns how many sessions were removed.
func (s *Service) PurgeExpired(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.Do(ctx, func(tx *store.Tx) error {
		now := tx.Now()
		res, err := tx.Exec(`DELETE FROM sessions WHERE idle_expires_at <= ? OR abs_expires_at <= ?`, now, now)
		if err != nil {
			return fmt.Errorf("purge sessions: %w", err)
		}
		n, _ = res.RowsAffected()
		if _, err := tx.Exec(`DELETE FROM password_resets WHERE expires_at <= ?`, now-7*24*time.Hour.Milliseconds()); err != nil {
			return fmt.Errorf("purge password resets: %w", err)
		}
		return nil
	})
	return n, err
}

// verifyCredentials is the rate-limited password check shared by Login and
// DeviceLogin: one limiter reservation per attempt, one dummy hash for every
// kind of failure. It returns the verified user, the stored hash and, when
// the stored hash is outdated, a fresh one to persist.
func (s *Service) verifyCredentials(ctx context.Context, in LoginInput) (*User, string, string, error) {
	username := NormalizeUsername(in.Username)
	if len(username) > maxLoginName {
		username = username[:maxLoginName]
	}
	// The attempt is reserved before anything is checked, so a burst of
	// concurrent logins cannot all reach the password check.
	attempt, wait, blocked := s.logins.Begin(username, in.IP)
	if blocked {
		return nil, "", "", &RateLimitedError{RetryAfter: wait}
	}

	user, pwHash, err := s.credentials(ctx, username)
	if err != nil {
		attempt.Abort()
		return nil, "", "", err
	}
	ok := false
	switch {
	case user != nil && user.Active() && user.Kind == KindHuman && pwHash != "" && utf8.RuneCountInString(in.Password) <= MaxPasswordLen:
		ok, err = s.verify(ctx, in.Password, pwHash)
		if err != nil {
			if ctx.Err() != nil {
				attempt.Abort()
				return nil, "", "", ctx.Err()
			}
			s.logger.Warn("unusable password hash", "user_id", user.ID)
			ok = false
		}
	default:
		s.verifyDummy(ctx, in.Password)
	}
	if !ok {
		s.auditLoginFailure(ctx, user, username, in.IP)
		return nil, "", "", ErrInvalidCredentials
	}
	attempt.Succeed()

	newHash := ""
	if s.hasher.NeedsRehash(pwHash) {
		newHash, _ = s.hasher.Hash(ctx, in.Password)
	}
	return user, pwHash, newHash, nil
}
