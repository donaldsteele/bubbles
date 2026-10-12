package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/store"
)

const (
	// HandoffTTL is how long a handoff code can be redeemed.
	HandoffTTL = 60 * time.Second

	maxPendingHandoffs = 1000
)

type handoffEntry struct {
	userID   string
	deviceID string
	expires  time.Time
}

// handoffStore keeps the pending one-time codes in memory, keyed by the
// SHA-256 of the code. A restart simply invalidates them.
type handoffStore struct {
	mu sync.Mutex
	m  map[[32]byte]handoffEntry
}

func (s *Service) newHandoff(userID, deviceID string) (string, error) {
	code, err := NewToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	h := &s.handoffs
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = map[[32]byte]handoffEntry{}
	}
	if len(h.m) >= maxPendingHandoffs {
		for k, e := range h.m {
			if !now.Before(e.expires) {
				delete(h.m, k)
			}
		}
		if len(h.m) >= maxPendingHandoffs {
			return "", &RateLimitedError{RetryAfter: HandoffTTL}
		}
	}
	h.m[[32]byte(HashToken(code))] = handoffEntry{userID: userID, deviceID: deviceID, expires: now.Add(HandoffTTL)}
	return code, nil
}

// IssueHandoff returns a new one-time code for the device behind p.
func (s *Service) IssueHandoff(p *Principal) (string, error) {
	if p == nil || p.Device == nil || p.User == nil {
		return "", ErrNotFound
	}
	return s.newHandoff(p.User.ID, p.Device.ID)
}

// RedeemHandoff consumes a handoff code and starts a web session for the
// user it was issued to (same cookie attributes and rotation as Login).
// Unknown, expired, already used codes, and codes whose device was revoked
// in the meantime, are ErrNotFound.
func (s *Service) RedeemHandoff(ctx context.Context, code, userAgent, ip, replaceToken string) (*SessionResult, error) {
	if code == "" || len(code) > maxTokenLen {
		return nil, ErrNotFound
	}
	key := [32]byte(HashToken(code))
	h := &s.handoffs
	h.mu.Lock()
	e, ok := h.m[key]
	delete(h.m, key)
	h.mu.Unlock()
	if !ok || !s.now().Before(e.expires) {
		return nil, ErrNotFound
	}
	var res SessionResult
	err := s.db.Do(ctx, func(tx *store.Tx) error {
		var revoked sql.NullInt64
		row := tx.QueryRow(`SELECT `+userColumns("u.")+`, d.revoked_at FROM device_tokens d JOIN users u ON u.id = d.user_id
			WHERE d.id = ? AND d.user_id = ? AND u.deactivated_at IS NULL`, e.deviceID, e.userID)
		user, err := scanUser(row, &revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load handoff device: %w", err)
		}
		if revoked.Valid {
			return ErrNotFound
		}
		if replaceToken != "" {
			if err := revokeByToken(tx, replaceToken); err != nil {
				return err
			}
		}
		token, sess, err := s.createSession(tx, user.ID, SessionHandoff, userAgent, ip)
		if err != nil {
			return err
		}
		res = SessionResult{Token: token, User: user, Session: sess}
		_, err = audit.Write(tx, audit.User(user.ID, ip).Entry(audit.ActionHandoff, "session", sess.ID, map[string]string{"device_id": e.deviceID}))
		return err
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}
