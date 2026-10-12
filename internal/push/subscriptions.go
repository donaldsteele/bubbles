package push

import (
	"context"
	"crypto/ecdh"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/donaldsteele/bubbles/internal/store"
)

// MaxSubscriptionsPerUser bounds the browsers one user can register.
const MaxSubscriptionsPerUser = 10

// maxUserAgentLen bounds the stored User-Agent.
const maxUserAgentLen = 255

// ErrTooManySubscriptions is returned by Subscribe when the user already has
// MaxSubscriptionsPerUser subscriptions and the endpoint is not one of them.
var ErrTooManySubscriptions = errors.New("too many push subscriptions")

// ErrEndpointOwned is returned by Subscribe when the endpoint belongs to
// another user and the request does not carry that subscription's keys.
var ErrEndpointOwned = errors.New("push endpoint belongs to another account")

// ValidationError lists the request fields Subscribe rejected, field to
// reason; the field names are the JSON paths of the request body.
type ValidationError struct{ Fields map[string]string }

func (e *ValidationError) Error() string { return "push: invalid subscription" }

// SubscribeInput is a browser PushSubscription.
type SubscribeInput struct {
	Endpoint  string
	P256DH    string
	Auth      string
	UserAgent string
}

// Subscription is a stored Web Push subscription.
type Subscription struct {
	ID           string
	UserID       string
	Endpoint     string
	P256DH       string
	Auth         string
	UserAgent    string
	CreatedAt    int64
	LastOKAt     *int64
	FailureCount int
}

// Subscribe stores in for userID, or refreshes the row with the same endpoint
// (a browser that re-subscribes clears its failure count; another account
// signing in on the same browser takes the row over only with identical keys,
// otherwise ErrEndpointOwned). Invalid input is
// a *ValidationError; a user at the limit gets ErrTooManySubscriptions.
func (s *Service) Subscribe(ctx context.Context, userID string, in SubscribeInput) error {
	fields := map[string]string{}
	if err := ValidateEndpoint(in.Endpoint); err != nil {
		fields["endpoint"] = err.Error()
	}
	p256dh, err := decodeKey(in.P256DH)
	if err != nil {
		fields["keys.p256dh"] = "is not base64url"
	} else if _, err := ecdh.P256().NewPublicKey(p256dh); err != nil {
		fields["keys.p256dh"] = "is not a valid P-256 public key"
	}
	auth, err := decodeKey(in.Auth)
	if err != nil {
		fields["keys.auth"] = "is not base64url"
	} else if len(auth) != 16 {
		fields["keys.auth"] = "must be 16 bytes"
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	ua := strings.ToValidUTF8(in.UserAgent, "")
	if len(ua) > maxUserAgentLen {
		ua = strings.ToValidUTF8(ua[:maxUserAgentLen], "")
	}
	p256dhText := base64.RawURLEncoding.EncodeToString(p256dh)
	authText := base64.RawURLEncoding.EncodeToString(auth)
	return s.db.Do(ctx, func(tx *store.Tx) error {
		var owner, oldKey, oldAuth string
		err := tx.QueryRow(`SELECT user_id, p256dh, auth FROM push_subscriptions WHERE endpoint = ?`, in.Endpoint).Scan(&owner, &oldKey, &oldAuth)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("look up push subscription: %w", err)
		}
		if owner != "" && owner != userID {
			// Taking an endpoint over from another account (the same browser
			// signing in as someone else) needs the browser's own keys: a
			// caller that only knows the endpoint URL cannot hijack it.
			if oldKey != p256dhText || oldAuth != authText {
				return ErrEndpointOwned
			}
			if _, err := tx.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, in.Endpoint); err != nil {
				return fmt.Errorf("replace push subscription: %w", err)
			}
		}
		if owner != userID {
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM push_subscriptions WHERE user_id = ?`, userID).Scan(&n); err != nil {
				return fmt.Errorf("count push subscriptions: %w", err)
			}
			if n >= MaxSubscriptionsPerUser {
				return ErrTooManySubscriptions
			}
		}
		_, err = tx.Exec(`
			INSERT INTO push_subscriptions (id, user_id, endpoint, p256dh, auth, user_agent, created_at, failure_count)
			VALUES (?, ?, ?, ?, ?, ?, ?, 0)
			ON CONFLICT (endpoint) DO UPDATE SET
				user_id = excluded.user_id, p256dh = excluded.p256dh, auth = excluded.auth,
				user_agent = excluded.user_agent, failure_count = 0`,
			tx.NewID(), userID, in.Endpoint, p256dhText, authText, ua, tx.Now())
		if err != nil {
			return fmt.Errorf("store push subscription: %w", err)
		}
		return nil
	})
}

// Unsubscribe removes userID's subscription with endpoint. Removing one that
// does not exist, or that belongs to someone else, is a no-op.
func (s *Service) Unsubscribe(ctx context.Context, userID, endpoint string) error {
	err := s.db.Do(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ? AND user_id = ?`, endpoint, userID)
		return err
	})
	if err != nil {
		return fmt.Errorf("remove push subscription: %w", err)
	}
	return nil
}

// HasSubscriptions reports whether userID has at least one subscription. A
// database error counts as none: the dispatcher skips the push.
func (s *Service) HasSubscriptions(ctx context.Context, userID string) bool {
	var one int
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT 1 FROM push_subscriptions WHERE user_id = ? LIMIT 1`, userID).Scan(&one)
	return err == nil
}

// Subscriptions lists userID's subscriptions, oldest first.
func (s *Service) Subscriptions(ctx context.Context, userID string) ([]Subscription, error) {
	rows, err := s.db.Read().QueryContext(ctx, `
		SELECT id, user_id, endpoint, p256dh, auth, user_agent, created_at, last_ok_at, failure_count
		FROM push_subscriptions WHERE user_id = ? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list push subscriptions: %w", err)
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		var sub Subscription
		var lastOK sql.NullInt64
		if err := rows.Scan(&sub.ID, &sub.UserID, &sub.Endpoint, &sub.P256DH, &sub.Auth, &sub.UserAgent,
			&sub.CreatedAt, &lastOK, &sub.FailureCount); err != nil {
			return nil, fmt.Errorf("list push subscriptions: %w", err)
		}
		if lastOK.Valid {
			sub.LastOKAt = &lastOK.Int64
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// decodeKey decodes a base64url subscription key, with or without padding. The
// standard alphabet is accepted too, as some clients send it.
func decodeKey(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}
