package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
)

// Errors returned by Service. Handlers map them to the REST error model.
var (
	// ErrInvalidCredentials is the one answer to every failed login.
	ErrInvalidCredentials = errors.New("invalid username or password")
	// ErrWrongPassword means the current password given to a password
	// change is incorrect.
	ErrWrongPassword = errors.New("current password is incorrect")
	// ErrUsernameTaken means the requested username exists.
	ErrUsernameTaken = errors.New("username is already taken")
	// ErrNameTaken means another active person already has that name.
	ErrNameTaken = errors.New("name is already taken")
	// ErrInviteInvalid means an invite code is unknown, expired, revoked or
	// used up.
	ErrInviteInvalid = errors.New("invite is invalid or has expired")
	// ErrResetInvalid means a password-reset code is unknown, expired or
	// already used.
	ErrResetInvalid = errors.New("password reset link is invalid or has expired")
	// ErrNotFound means the addressed user, session or invite does not exist.
	ErrNotFound = errors.New("not found")
	// ErrRegistrationClosed means open registration is switched off.
	ErrRegistrationClosed = errors.New("registration is closed")
)

// RateLimitedError carries the wait before the caller may retry.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "too many attempts; try again later" }

// ValidationError lists the request fields that were rejected and why.
type ValidationError struct{ Fields map[string]string }

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for f, why := range e.Fields {
		parts = append(parts, f+": "+why)
	}
	return "invalid request: " + strings.Join(parts, "; ")
}

// Options configure a Service.
type Options struct {
	// DB is the store; required.
	DB *store.DB
	// Settings supplies session_idle_days, open_registration and
	// instance_name; required.
	Settings *settings.Store
	// DataDir holds admin-password.txt; BootstrapAdmin skips the file when
	// it is empty.
	DataDir string
	// Now is the clock; nil means time.Now. It should be the clock the store
	// uses, so session expiry and created_at agree.
	Now func() time.Time
	// Logger receives non-fatal errors; nil discards them.
	Logger *slog.Logger
	// Params are the argon2id parameters for new hashes; the zero value
	// means DefaultParams. Tests inject cheap ones.
	Params Params
	// MaxConcurrentHashes bounds simultaneous argon2 computations; 0 means
	// DefaultMaxConcurrentHashes.
	MaxConcurrentHashes int
	// OnBootstrap runs inside the bootstrap transaction after the first admin
	// exists; the chat service uses it to create #general. An error aborts
	// the bootstrap.
	OnBootstrap func(ctx context.Context, tx *store.Tx, adminID string) error
	// OnUserCreated runs inside the transaction that creates a human account:
	// the bootstrap admin (after OnBootstrap), an invite redemption and a registration.
	// An error aborts that transaction. nil means no-op.
	OnUserCreated func(ctx context.Context, tx *store.Tx, userID string) error
	// LoginLimiter and APILimiter replace the default rate limiters.
	LoginLimiter *LoginLimiter
	APILimiter   *BucketLimiter
	// RegisterLimiter replaces the per-IP limit on sign-up attempts (register
	// and device-register); nil means 20 attempts per IP per hour.
	RegisterLimiter *FailureLimiter
	// AdminPassword fixes the password BootstrapAdmin sets instead of a
	// generated one. Tests and the e2e build use it; empty means generate.
	AdminPassword string
}

// Service is the auth domain service.
type Service struct {
	db       *store.DB
	settings *settings.Store
	dataDir  string
	now      func() time.Time
	logger   *slog.Logger
	hasher   *Hasher
	// verify checks a password against an encoded hash. It is the hasher's
	// Verify; tests wrap it to count password checks.
	verify        func(ctx context.Context, password, encoded string) (bool, error)
	onBootstrap   func(ctx context.Context, tx *store.Tx, adminID string) error
	onUserCreated func(ctx context.Context, tx *store.Tx, userID string) error
	logins        *LoginLimiter
	api           *BucketLimiter
	signups       *FailureLimiter
	devices       deviceState
	handoffs      handoffStore

	dummyOnce sync.Once
	dummyHash string

	adminPassword string // fixed bootstrap password; empty means generate
}

// New returns a Service.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Settings == nil {
		return nil, errors.New("auth: DB and Settings are required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Params == (Params{}) {
		o.Params = DefaultParams
	}
	if o.LoginLimiter == nil {
		o.LoginLimiter = NewLoginLimiter(o.Now)
	}
	if o.APILimiter == nil {
		o.APILimiter = NewAPILimiter(o.Now)
	}
	if o.RegisterLimiter == nil {
		o.RegisterLimiter = NewFailureLimiter(o.Now, SignupIPMaxAttempts, SignupWindow)
	}
	hasher := NewHasher(o.Params, o.MaxConcurrentHashes)
	svc := &Service{
		db: o.DB, settings: o.Settings, dataDir: o.DataDir, now: o.Now, logger: o.Logger,
		hasher: hasher, verify: hasher.Verify,
		onBootstrap: o.OnBootstrap, adminPassword: o.AdminPassword, onUserCreated: o.OnUserCreated, logins: o.LoginLimiter, api: o.APILimiter, signups: o.RegisterLimiter,
	}
	// Accounts from before display keys existed get theirs now; a failure
	// only costs login-by-name for them, so it must not stop startup.
	if err := svc.backfillDisplayKeys(context.Background()); err != nil {
		o.Logger.Warn("backfill display keys", "err", err)
	}
	return svc, nil
}

// userCreated runs the OnUserCreated hook inside the transaction that created
// a human account. Its error aborts that transaction.
func (s *Service) userCreated(ctx context.Context, tx *store.Tx, userID string) error {
	if s.onUserCreated == nil {
		return nil
	}
	if err := s.onUserCreated(ctx, tx, userID); err != nil {
		return fmt.Errorf("user created hook: %w", err)
	}
	return nil
}

// Now returns the service clock; sign-up deadlines are judged against it.
func (s *Service) Now() time.Time { return s.now() }

// Settings returns the settings store the service reads.
func (s *Service) Settings() *settings.Store { return s.settings }

// APILimiter returns the per-session API rate limiter (20 req/s, burst 50).
func (s *Service) APILimiter() *BucketLimiter { return s.api }

// HashPassword hashes a password that already passed ValidatePassword.
func (s *Service) HashPassword(ctx context.Context, password string) (string, error) {
	return s.hasher.Hash(ctx, password)
}

// verifyDummy burns the same time as a real verification, so a login for an
// unknown user is not faster than one for a known user.
func (s *Service) verifyDummy(ctx context.Context, password string) {
	s.dummyOnce.Do(func() {
		s.dummyHash, _ = s.hasher.Hash(context.Background(), "bubbles-dummy-password")
	})
	if s.dummyHash != "" {
		_, _ = s.verify(ctx, password, s.dummyHash)
	}
}

// Username and display-name rules (the users table CHECKs are the backstop).
var usernameRE = regexp.MustCompile(`^[a-z0-9._-]{2,32}$`)

const maxDisplayNameLen = 64

// NormalizeUsername lower-cases and trims a username.
func NormalizeUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// newUserFields holds validated, normalized input for a new account.
type newUserFields struct {
	username    string // empty: derived from the display name at insert
	displayName string
	displayKey  string
	password    string
	// uniqueName makes insertUser reject a display name another active
	// human already uses.
	uniqueName bool
}

// validateSignup validates an invite redemption: the
// name-first form when name is set, the username form otherwise. Either way
// the display name must be unique among active humans.
func validateSignup(name, username, displayName, password string) (newUserFields, *ValidationError) {
	if name != "" {
		return validateNewName(name, password)
	}
	f, verr := validateNewUser(username, displayName, password)
	f.uniqueName = true
	return f, verr
}

// validateNewUser checks the fields of a signup (invite, register).
func validateNewUser(username, displayName, password string) (newUserFields, *ValidationError) {
	fields := map[string]string{}
	f := newUserFields{username: NormalizeUsername(username), password: password}
	if !usernameRE.MatchString(f.username) {
		fields["username"] = "must be 2 to 32 characters: lowercase letters, digits, '.', '_' or '-'"
	}
	name, why := normalizeDisplayName(displayName)
	if why != "" {
		fields["display_name"] = why
	}
	f.displayName = name
	if f.displayName == "" {
		f.displayName = f.username
	}
	f.displayKey = displayKey(f.displayName)
	if why := passwordField(password); why != "" {
		fields["password"] = why
	}
	if len(fields) > 0 {
		return f, &ValidationError{Fields: fields}
	}
	return f, nil
}

// normalizeNote trims an invite note.
func normalizeNote(s string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > 200 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", &ValidationError{Fields: map[string]string{"note": "must be at most 200 characters without control characters"}}
	}
	return s, nil
}
