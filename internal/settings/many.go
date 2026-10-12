package settings

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/store"
)

// SetMany validates every entry first and then stores all of them in one
// transaction, so a rejected value changes nothing. Each key is audited.
func (s *Store) SetMany(ctx context.Context, actor audit.Actor, values map[string]json.RawMessage) error {
	keys := make([]string, 0, len(values))
	for k, raw := range values {
		if err := Validate(k, raw); err != nil {
			return err
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return s.db.Do(ctx, func(tx *store.Tx) error {
		for _, k := range keys {
			if err := s.SetTx(tx, actor, k, values[k]); err != nil {
				return err
			}
		}
		return nil
	})
}

// WindowEnd is the sign-up deadline for a window opened at now: nil when
// hours is 0 (no deadline).
func WindowEnd(now time.Time, hours int) *time.Time {
	if hours <= 0 {
		return nil
	}
	t := now.UTC().Add(time.Duration(hours) * time.Hour)
	return &t
}

// SetManyAt is SetMany plus the sign-up deadline rules, all in one
// transaction. Switching open_registration on starts a new window (now plus
// registration_window_hours), switching it off clears the deadline, and
// changing registration_window_hours while sign-up is open re-bases the
// deadline on now. open_registration_until is not a patchable key: callers
// reject it before this point.
func (s *Store) SetManyAt(ctx context.Context, actor audit.Actor, now time.Time, values map[string]json.RawMessage) error {
	prev := s.Values()
	open, window := prev.OpenRegistration, prev.RegistrationWindowH
	var wantOpen, wantWindow bool
	if raw, ok := values[KeyOpenRegistration]; ok {
		_ = json.Unmarshal(raw, &open)
		wantOpen = true
	}
	if raw, ok := values[KeyRegistrationWindowH]; ok {
		var n int
		if json.Unmarshal(raw, &n) == nil {
			window, wantWindow = n, true
		}
	}
	merged := make(map[string]json.RawMessage, len(values)+1)
	for k, v := range values {
		merged[k] = v
	}
	var until *time.Time
	set := false
	switch {
	case wantOpen && open && !prev.OpenRegistration, wantWindow && open:
		until, set = WindowEnd(now, window), true
	case wantOpen && !open:
		set = prev.OpenRegistrationEnds != nil
	}
	if set {
		raw, _ := json.Marshal(DeadlineValue(until))
		merged[KeyOpenRegistrationEnds] = raw
	}
	return s.SetMany(ctx, actor, merged)
}

// ReopenRegistration switches sign-up on and starts a new window at now.
func (s *Store) ReopenRegistration(ctx context.Context, actor audit.Actor, now time.Time) error {
	until := WindowEnd(now, s.Values().RegistrationWindowH)
	return s.db.Do(ctx, func(tx *store.Tx) error {
		if err := s.SetTx(tx, actor, KeyOpenRegistration, true); err != nil {
			return err
		}
		if err := s.SetTx(tx, actor, KeyOpenRegistrationEnds, DeadlineValue(until)); err != nil {
			return err
		}
		_, err := audit.Write(tx, actor.Entry(audit.ActionRegistrationOpen, "setting", KeyOpenRegistration,
			map[string]any{"closes_at": DeadlineValue(until)}))
		return err
	})
}

// CloseRegistration switches sign-up off and clears the deadline.
func (s *Store) CloseRegistration(ctx context.Context, actor audit.Actor) error {
	return s.db.Do(ctx, func(tx *store.Tx) error {
		if err := s.SetTx(tx, actor, KeyOpenRegistration, false); err != nil {
			return err
		}
		if err := s.SetTx(tx, actor, KeyOpenRegistrationEnds, nil); err != nil {
			return err
		}
		_, err := audit.Write(tx, actor.Entry(audit.ActionRegistrationClose, "setting", KeyOpenRegistration, nil))
		return err
	})
}
