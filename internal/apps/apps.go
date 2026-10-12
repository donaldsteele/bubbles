// Package apps is the integration domain: app accounts (a bot user plus the
// app row), their scoped bearer tokens, the bearer authenticator, per-token
// rate limits and the pairing flow that hands a new app its first token.
//
// Like the other domain services, every mutation runs in one store
// transaction together with its audit entry and events; the Tx variants join
// the caller's transaction.
package apps

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Audit actions written by this package. Detail never carries a token, a
// poll secret or a pairing ciphertext.
const (
	ActionAppCreate      = "app.create"
	ActionAppUpdate      = "app.update"
	ActionAppDisable     = "app.disable"
	ActionAppEnable      = "app.enable"
	ActionTokenCreate    = "token.create"
	ActionTokenRevoke    = "token.revoke"
	ActionPairingApprove = "pairing.approve"
	ActionPairingDeny    = "pairing.deny"
)

// Limits of app fields.
const (
	MaxNameChars        = 64
	MaxDescriptionChars = 500
)

// Options configure a Service.
type Options struct {
	DB   *store.DB
	Chat *chat.Service
	// Now is the clock of the limiters, token expiry and pairing; nil means
	// time.Now. It should be the clock the store uses.
	Now    func() time.Time
	Logger *slog.Logger
	// Realtime delivers the ephemeral command events and reports which apps are
	// connected; it is the events hub. nil disables command invocation.
	Realtime Realtime
}

// Service is the apps domain service.
type Service struct {
	db     *store.DB
	chat   *chat.Service
	now    func() time.Time
	logger *slog.Logger
	rt     Realtime

	// invMu guards the invocation state: the map, the expiry queue (oldest
	// first; every invocation lives InvocationTTL, so it is in expiry order)
	// and the pending counts per app and per invoker.
	invMu        sync.Mutex
	invocations  map[string]*invocation
	invQueue     []pendingRef
	invByApp     map[string]int
	invByInvoker map[string]int

	// tokens limits requests per token: 10 per second, burst 30.
	tokens *auth.BucketLimiter
	// pairCreate and pairPoll limit the anonymous pairing endpoints per
	// source address.
	pairCreate *auth.BucketLimiter
	pairPoll   *auth.BucketLimiter

	mu      sync.Mutex
	touched map[string]int64 // token id -> unix ms of the last last_used_at write
}

// New returns a Service. DB and Chat are required.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		db: o.DB, chat: o.Chat, now: o.Now, logger: o.Logger, rt: o.Realtime,
		invocations: map[string]*invocation{}, invByApp: map[string]int{}, invByInvoker: map[string]int{},
		tokens:     auth.NewBucketLimiter(o.Now, TokenRatePerSecond, TokenBurst),
		pairCreate: auth.NewBucketLimiter(o.Now, PairingCreatesPerMinute/60.0, PairingCreatesPerMinute),
		pairPoll:   auth.NewBucketLimiter(o.Now, PairingPollsPerSecond, PairingPollBurst),
		touched:    map[string]int64{},
	}
}

// queryer is the read side shared by *sql.DB and *store.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type scanner interface{ Scan(dest ...any) error }

// run executes fn in one write transaction.
func run[T any](ctx context.Context, s *Service, fn func(tx *store.Tx) (T, error)) (T, error) {
	var out T
	err := s.db.Do(ctx, func(tx *store.Tx) error {
		v, err := fn(tx)
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

// CreateAppInput are the inputs of CreateApp.
type CreateAppInput struct {
	Name        string
	Description string
	// Username is the bot's username; empty derives one from Name.
	Username string
	// OwnerID is the admin who created the app.
	OwnerID string
}

// CreateApp creates an app and its bot account (kind app, role member, no
// password, so it can never sign in with a cookie).
func (s *Service) CreateApp(ctx context.Context, actor audit.Actor, in CreateAppInput) (protocol.App, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.App, error) { return s.CreateAppTx(tx, actor, in) })
}

// CreateAppTx is CreateApp inside the caller's transaction.
func (s *Service) CreateAppTx(tx *store.Tx, actor audit.Actor, in CreateAppInput) (protocol.App, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return protocol.App{}, err
	}
	desc, err := cleanDescription(in.Description)
	if err != nil {
		return protocol.App{}, err
	}
	username, err := chooseUsername(tx, in.Username, name)
	if err != nil {
		return protocol.App{}, err
	}
	userID, appID, now := tx.NewID(), tx.NewID(), tx.Now()
	_, err = tx.Exec(`INSERT INTO users (id, username, display_name, kind, role, created_at) VALUES (?, ?, ?, 'app', 'member', ?)`,
		userID, username, name, now)
	if err != nil {
		return protocol.App{}, fmt.Errorf("insert bot user: %w", err)
	}
	_, err = tx.Exec(`INSERT INTO apps (id, user_id, name, description, owner_user_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		appID, userID, name, desc, nullStr(in.OwnerID), now)
	if err != nil {
		return protocol.App{}, fmt.Errorf("insert app: %w", err)
	}
	if _, err := audit.Write(tx, actor.Entry(ActionAppCreate, "app", appID, map[string]string{"name": name, "username": username})); err != nil {
		return protocol.App{}, err
	}
	return loadApp(tx.Context(), tx, appID)
}

// ListApps returns every app with its tokens, oldest first.
func (s *Service) ListApps(ctx context.Context) ([]protocol.App, error) {
	db := s.db.Read()
	rows, err := db.QueryContext(ctx, appSelect+` ORDER BY a.id`)
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	defer rows.Close()
	var out []protocol.App
	byID := map[string]int{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, fmt.Errorf("list apps: %w", err)
		}
		byID[a.ID] = len(out)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	trows, err := db.QueryContext(ctx, tokenSelect+` ORDER BY t.id`)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer trows.Close()
	for trows.Next() {
		t, err := scanToken(trows)
		if err != nil {
			return nil, fmt.Errorf("list tokens: %w", err)
		}
		if i, ok := byID[t.AppID]; ok {
			out[i].Tokens = append(out[i].Tokens, t)
		}
	}
	if err := trows.Err(); err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	if out == nil {
		out = []protocol.App{}
	}
	return out, nil
}

// GetApp returns one app with its tokens, or ErrNotFound.
func (s *Service) GetApp(ctx context.Context, id string) (protocol.App, error) {
	return loadApp(ctx, s.db.Read(), id)
}

// UpdateAppInput are the optional changes of UpdateApp.
type UpdateAppInput struct {
	Name        *string
	Description *string
	Disabled    *bool
}

// UpdateApp renames an app, edits its description, or disables and enables
// it. A disabled app's tokens are rejected on their next request and its live
// connections are closed; nothing is revoked, so enabling the app restores them.
func (s *Service) UpdateApp(ctx context.Context, actor audit.Actor, id string, in UpdateAppInput) (protocol.App, error) {
	return run(ctx, s, func(tx *store.Tx) (protocol.App, error) {
		ctx := tx.Context()
		cur, err := loadApp(ctx, tx, id)
		if err != nil {
			return protocol.App{}, err
		}
		name, desc := cur.Name, cur.Description
		if in.Name != nil {
			if name, err = cleanName(*in.Name); err != nil {
				return protocol.App{}, err
			}
		}
		if in.Description != nil {
			if desc, err = cleanDescription(*in.Description); err != nil {
				return protocol.App{}, err
			}
		}
		if _, err := tx.Exec(`UPDATE apps SET name = ?, description = ? WHERE id = ?`, name, desc, id); err != nil {
			return protocol.App{}, fmt.Errorf("update app: %w", err)
		}
		if name != cur.Name {
			if _, err := tx.Exec(`UPDATE users SET display_name = ? WHERE id = ?`, name, cur.UserID); err != nil {
				return protocol.App{}, fmt.Errorf("update bot user: %w", err)
			}
		}
		action := ActionAppUpdate
		if in.Disabled != nil {
			wasDisabled := cur.DisabledAt != nil
			switch {
			case *in.Disabled && !wasDisabled:
				_, err = tx.Exec(`UPDATE apps SET disabled_at = ? WHERE id = ?`, tx.Now(), id)
				action = ActionAppDisable
			case !*in.Disabled && wasDisabled:
				_, err = tx.Exec(`UPDATE apps SET disabled_at = NULL WHERE id = ?`, id)
				action = ActionAppEnable
			}
			if err != nil {
				return protocol.App{}, fmt.Errorf("update app: %w", err)
			}
			if action == ActionAppDisable {
				// Close the app's live connections when the disable commits.
				if err := appendBearerClose(tx, cur.UserID, map[string]any{"app_id": id, "app_disabled": true}); err != nil {
					return protocol.App{}, err
				}
			}
		}
		if _, err := audit.Write(tx, actor.Entry(action, "app", id, map[string]string{"name": name})); err != nil {
			return protocol.App{}, err
		}
		return loadApp(ctx, tx, id)
	})
}

const appSelect = `SELECT a.id, a.user_id, u.username, a.name, a.description, a.owner_user_id, a.created_at, a.disabled_at
	FROM apps a JOIN users u ON u.id = a.user_id`

func scanApp(sc scanner) (protocol.App, error) {
	var (
		a        protocol.App
		owner    sql.NullString
		created  int64
		disabled sql.NullInt64
	)
	if err := sc.Scan(&a.ID, &a.UserID, &a.Username, &a.Name, &a.Description, &owner, &created, &disabled); err != nil {
		return a, err
	}
	if owner.Valid {
		a.OwnerUserID = &owner.String
	}
	a.CreatedAt = protocol.FormatTime(created)
	if disabled.Valid {
		a.DisabledAt = msPtr(disabled.Int64)
	}
	a.Tokens = []protocol.AppToken{}
	return a, nil
}

// loadApp returns the app with its tokens, or ErrNotFound.
func loadApp(ctx context.Context, q queryer, id string) (protocol.App, error) {
	a, err := scanApp(q.QueryRowContext(ctx, appSelect+` WHERE a.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, notFound()
	}
	if err != nil {
		return a, fmt.Errorf("load app: %w", err)
	}
	rows, err := q.QueryContext(ctx, tokenSelect+` WHERE t.app_id = ? ORDER BY t.id`, id)
	if err != nil {
		return a, fmt.Errorf("load tokens: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return a, fmt.Errorf("load tokens: %w", err)
		}
		a.Tokens = append(a.Tokens, t)
	}
	return a, rows.Err()
}

// chooseUsername validates an explicit username or derives a free one from
// the app name.
func chooseUsername(tx *store.Tx, explicit, name string) (string, error) {
	if explicit != "" {
		u := auth.NormalizeUsername(explicit)
		if !validUsername(u) {
			return "", invalid("username", "username must be 2-32 characters: lowercase letters, digits, '.', '_' or '-'")
		}
		taken, err := usernameTaken(tx, u)
		if err != nil {
			return "", err
		}
		if taken {
			return "", &chat.ValidationError{Fields: map[string]string{"username": "already taken"}}
		}
		return u, nil
	}
	base := deriveUsername(name)
	for i := 1; i <= 99; i++ {
		cand := base
		if i > 1 {
			suffix := fmt.Sprintf("-%d", i)
			cand = base[:min(len(base), 32-len(suffix))] + suffix
		}
		taken, err := usernameTaken(tx, cand)
		if err != nil {
			return "", err
		}
		if !taken {
			return cand, nil
		}
	}
	return "", conflict("no free username for this app name; give a username")
}

func usernameTaken(tx *store.Tx, username string) (bool, error) {
	var one int
	err := tx.QueryRow(`SELECT 1 FROM users WHERE username = ?`, username).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check username: %w", err)
	}
	return true, nil
}

func validUsername(u string) bool {
	if len(u) < 2 || len(u) > 32 {
		return false
	}
	for i := 0; i < len(u); i++ {
		c := u[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// deriveUsername turns an app name into a valid username: lower-case ASCII
// letters and digits, runs of anything else become one hyphen.
func deriveUsername(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '.' || r == '_':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = true
		}
	}
	u := strings.Trim(b.String(), "-._")
	if len(u) > 32 {
		u = strings.Trim(u[:32], "-._")
	}
	if len(u) < 2 {
		u = "app-" + u
		u = strings.TrimRight(u, "-")
	}
	return u
}

// cleanName trims and validates an app name.
func cleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", invalid("name", "name is required")
	case !validText(s):
		return "", invalid("name", "name must be valid text")
	case utf8.RuneCountInString(s) > MaxNameChars:
		return "", invalid("name", fmt.Sprintf("name must be at most %d characters", MaxNameChars))
	}
	return s, nil
}

func cleanDescription(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case !validText(s):
		return "", invalid("description", "description must be valid text")
	case utf8.RuneCountInString(s) > MaxDescriptionChars:
		return "", invalid("description", fmt.Sprintf("description must be at most %d characters", MaxDescriptionChars))
	}
	return s, nil
}

// validText reports whether s is valid UTF-8 without control characters
// other than tab and newline.
func validText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func msPtr(ms int64) *string {
	s := protocol.FormatTime(ms)
	return &s
}

func nullMs(n sql.NullInt64) *string {
	if !n.Valid {
		return nil
	}
	return msPtr(n.Int64)
}

// decodeStrings parses a JSON string array column.
func decodeStrings(raw string) ([]string, error) {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetRealtime installs the realtime hub after construction (the composition
// root and tests build the services first). Call it before serving requests.
func (s *Service) SetRealtime(rt Realtime) { s.rt = rt }
