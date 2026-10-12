// Package settings reads and writes the instance settings stored in the
// settings table. Every key has a typed accessor with a default, values are
// cached in memory until the next Set, and Set validates the value, audits
// the change and appends a settings.updated event in one transaction.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/store"
)

// Setting keys.
const (
	KeyInstanceName         = "instance_name"
	KeyOpenRegistration     = "open_registration"
	KeyUploadMaxBytes       = "upload_max_bytes"
	KeyUserQuotaBytes       = "user_quota_bytes"
	KeyDiskReserveBytes     = "disk_reserve_bytes"
	KeyDefaultRetentionDays = "default_retention_days"
	KeyWebPushEnabled       = "web_push_enabled"
	KeyPushPayload          = "push_payload"
	KeyAllowedCIDRs         = "allowed_cidrs"
	KeySessionIdleDays      = "session_idle_days"
	KeyAlertRenotifyS       = "alert_renotify_s"
	KeyMDNSEnabled          = "mdns_enabled"
	KeyWebResetEnabled      = "web_reset_enabled"
)

// Push payload modes.
const (
	PushMinimal = "minimal"
	PushFull    = "full"
)

// Limits enforced by Set.
const (
	MaxUploadBytes     = 2 << 30
	MaxInstanceNameLen = 64
	MaxSessionIdleDays = 90
	MaxRetentionDays   = 3650
)

// ErrInvalid is wrapped by every error that rejects a key or value.
var ErrInvalid = errors.New("invalid setting")

// Values is a snapshot of every setting with defaults applied.
type Values struct {
	InstanceName         string
	OpenRegistration     bool
	UploadMaxBytes       int64
	UserQuotaBytes       int64 // 0 = unlimited
	DiskReserveBytes     int64
	DefaultRetentionDays *int // nil = keep forever
	WebPushEnabled       bool
	PushPayload          string
	AllowedCIDRs         []netip.Prefix
	SessionIdleDays      int
	AlertRenotifyS       int
	MDNSEnabled          bool
	WebResetEnabled      bool
}

// Defaults returns the value of every setting on a fresh instance.
func Defaults() Values {
	return Values{
		InstanceName:     "bubbles",
		UploadMaxBytes:   100 << 20,
		UserQuotaBytes:   5 << 30,
		DiskReserveBytes: 1 << 30,
		PushPayload:      PushMinimal,
		SessionIdleDays:  30,
		AlertRenotifyS:   1800,
		MDNSEnabled:      true,
		WebResetEnabled:  true,
	}
}

// def validates one key and applies its value to a Values.
type def struct {
	key   string
	apply func(v *Values, raw json.RawMessage) error
}

func decode[T any](raw json.RawMessage) (T, error) {
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return out, nil
}

func rangeInt(name string, n, lo, hi int64) error {
	if n < lo || n > hi {
		return fmt.Errorf("%w: %s must be between %d and %d", ErrInvalid, name, lo, hi)
	}
	return nil
}

var defs = []def{
	{KeyInstanceName, func(v *Values, raw json.RawMessage) error {
		s, err := decode[string](raw)
		if err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if n := utf8.RuneCountInString(s); n < 1 || n > MaxInstanceNameLen {
			return fmt.Errorf("%w: instance_name must be 1 to %d characters", ErrInvalid, MaxInstanceNameLen)
		}
		if strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return fmt.Errorf("%w: instance_name must not contain control characters", ErrInvalid)
		}
		v.InstanceName = s
		return nil
	}},
	{KeyOpenRegistration, func(v *Values, raw json.RawMessage) (err error) {
		v.OpenRegistration, err = decode[bool](raw)
		return err
	}},
	{KeyUploadMaxBytes, func(v *Values, raw json.RawMessage) error {
		n, err := decode[int64](raw)
		if err != nil {
			return err
		}
		if err := rangeInt(KeyUploadMaxBytes, n, 1, MaxUploadBytes); err != nil {
			return err
		}
		v.UploadMaxBytes = n
		return nil
	}},
	{KeyUserQuotaBytes, func(v *Values, raw json.RawMessage) error {
		n, err := decode[int64](raw)
		if err != nil {
			return err
		}
		if n < 0 {
			return fmt.Errorf("%w: user_quota_bytes must not be negative", ErrInvalid)
		}
		v.UserQuotaBytes = n
		return nil
	}},
	{KeyDiskReserveBytes, func(v *Values, raw json.RawMessage) error {
		n, err := decode[int64](raw)
		if err != nil {
			return err
		}
		if n < 0 {
			return fmt.Errorf("%w: disk_reserve_bytes must not be negative", ErrInvalid)
		}
		v.DiskReserveBytes = n
		return nil
	}},
	{KeyDefaultRetentionDays, func(v *Values, raw json.RawMessage) error {
		p, err := decode[*int64](raw)
		if err != nil {
			return err
		}
		if p == nil {
			v.DefaultRetentionDays = nil
			return nil
		}
		if err := rangeInt(KeyDefaultRetentionDays, *p, 1, MaxRetentionDays); err != nil {
			return err
		}
		n := int(*p)
		v.DefaultRetentionDays = &n
		return nil
	}},
	{KeyWebPushEnabled, func(v *Values, raw json.RawMessage) (err error) {
		v.WebPushEnabled, err = decode[bool](raw)
		return err
	}},
	{KeyPushPayload, func(v *Values, raw json.RawMessage) error {
		s, err := decode[string](raw)
		if err != nil {
			return err
		}
		if s != PushMinimal && s != PushFull {
			return fmt.Errorf("%w: push_payload must be %q or %q", ErrInvalid, PushMinimal, PushFull)
		}
		v.PushPayload = s
		return nil
	}},
	{KeyAllowedCIDRs, func(v *Values, raw json.RawMessage) error {
		list, err := decode[[]string](raw)
		if err != nil {
			return err
		}
		out := make([]netip.Prefix, 0, len(list))
		for _, s := range list {
			p, err := netip.ParsePrefix(strings.TrimSpace(s))
			if err != nil {
				return fmt.Errorf("%w: allowed_cidrs: %q is not a CIDR range", ErrInvalid, s)
			}
			out = append(out, p.Masked())
		}
		v.AllowedCIDRs = out
		return nil
	}},
	{KeySessionIdleDays, func(v *Values, raw json.RawMessage) error {
		n, err := decode[int64](raw)
		if err != nil {
			return err
		}
		if err := rangeInt(KeySessionIdleDays, n, 1, MaxSessionIdleDays); err != nil {
			return err
		}
		v.SessionIdleDays = int(n)
		return nil
	}},
	{KeyAlertRenotifyS, func(v *Values, raw json.RawMessage) error {
		n, err := decode[int64](raw)
		if err != nil {
			return err
		}
		if err := rangeInt(KeyAlertRenotifyS, n, 10, 86400); err != nil {
			return err
		}
		v.AlertRenotifyS = int(n)
		return nil
	}},
	{KeyMDNSEnabled, func(v *Values, raw json.RawMessage) (err error) {
		v.MDNSEnabled, err = decode[bool](raw)
		return err
	}},
	{KeyWebResetEnabled, func(v *Values, raw json.RawMessage) (err error) {
		v.WebResetEnabled, err = decode[bool](raw)
		return err
	}},
}

func lookupDef(key string) (def, bool) {
	for _, d := range defs {
		if d.key == key {
			return d, true
		}
	}
	return def{}, false
}

// Keys returns every setting key.
func Keys() []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.key
	}
	return out
}

// loadTimeout bounds the cache refill triggered by an accessor.
const loadTimeout = 5 * time.Second

// Store gives typed, cached access to the settings table.
type Store struct {
	db *store.DB

	mu   sync.Mutex
	snap *Values // nil until loaded and after every Set
	gen  uint64  // bumped by Invalidate so a racing load is not cached
}

// New returns a Store over db.
func New(db *store.DB) *Store { return &Store{db: db} }

// Invalidate drops the cache; the next accessor reloads from the database.
// Set calls it after commit; call it after changing the table another way.
func (s *Store) Invalidate() {
	s.mu.Lock()
	s.snap = nil
	s.gen++
	s.mu.Unlock()
}

// Values returns every setting. Rows that fail validation (edited by hand)
// fall back to the default; if the table cannot be read the defaults are
// returned and nothing is cached.
func (s *Store) Values() Values {
	s.mu.Lock()
	if s.snap != nil {
		v := *s.snap
		s.mu.Unlock()
		return v
	}
	gen := s.gen
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	v, err := s.load(ctx)
	if err != nil {
		return Defaults()
	}
	s.mu.Lock()
	if s.gen == gen {
		s.snap = &v
	}
	s.mu.Unlock()
	return v
}

func (s *Store) load(ctx context.Context) (Values, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return Values{}, fmt.Errorf("read settings: %w", err)
	}
	defer rows.Close()
	v := Defaults()
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return Values{}, fmt.Errorf("read settings: %w", err)
		}
		d, ok := lookupDef(key)
		if !ok {
			continue
		}
		scratch := v
		if d.apply(&scratch, json.RawMessage(raw)) == nil {
			v = scratch
		}
	}
	if err := rows.Err(); err != nil {
		return Values{}, fmt.Errorf("read settings: %w", err)
	}
	return v, nil
}

// InstanceName is the display name of this server.
func (s *Store) InstanceName() string { return s.Values().InstanceName }

// OpenRegistration reports whether anyone may self-register.
func (s *Store) OpenRegistration() bool { return s.Values().OpenRegistration }

// UploadMaxBytes is the per-file upload limit.
func (s *Store) UploadMaxBytes() int64 { return s.Values().UploadMaxBytes }

// UserQuotaBytes is the per-user storage quota; 0 means unlimited.
func (s *Store) UserQuotaBytes() int64 { return s.Values().UserQuotaBytes }

// DiskReserveBytes is the free space uploads must leave.
func (s *Store) DiskReserveBytes() int64 { return s.Values().DiskReserveBytes }

// DefaultRetentionDays is the default channel retention; nil keeps forever.
func (s *Store) DefaultRetentionDays() *int { return s.Values().DefaultRetentionDays }

// WebPushEnabled reports whether Web Push is switched on.
func (s *Store) WebPushEnabled() bool { return s.Values().WebPushEnabled }

// PushPayload is PushMinimal or PushFull.
func (s *Store) PushPayload() string { return s.Values().PushPayload }

// AllowedCIDRs returns the extra source ranges admitted besides private
// addresses. The slice is shared: do not modify it.
func (s *Store) AllowedCIDRs() []netip.Prefix { return s.Values().AllowedCIDRs }

// SessionIdleDays is the idle expiry of sessions in days.
func (s *Store) SessionIdleDays() int { return s.Values().SessionIdleDays }

// AlertRenotifyS is the default alert renotify interval in seconds.
func (s *Store) AlertRenotifyS() int { return s.Values().AlertRenotifyS }

// MDNSEnabled reports whether mDNS advertising is on.
func (s *Store) MDNSEnabled() bool { return s.Values().MDNSEnabled }

// WebResetEnabled reports whether the admin UI may reset the instance.
func (s *Store) WebResetEnabled() bool { return s.Values().WebResetEnabled }

// Validate reports whether value is acceptable for key. The error wraps
// ErrInvalid.
func Validate(key string, value any) error {
	d, ok := lookupDef(key)
	if !ok {
		return fmt.Errorf("%w: unknown key %q", ErrInvalid, key)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	scratch := Defaults()
	return d.apply(&scratch, raw)
}

// Set validates value for key and stores it, audits the change and appends
// a settings.updated event, all in one transaction.
func (s *Store) Set(ctx context.Context, actor audit.Actor, key string, value any) error {
	return s.db.Do(ctx, func(tx *store.Tx) error {
		return s.SetTx(tx, actor, key, value)
	})
}

// SetTx is Set inside the caller's transaction (first-run setup uses it).
// The cache is dropped after the transaction commits.
func (s *Store) SetTx(tx *store.Tx, actor audit.Actor, key string, value any) error {
	d, ok := lookupDef(key)
	if !ok {
		return fmt.Errorf("%w: unknown key %q", ErrInvalid, key)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	scratch := Defaults()
	if err := d.apply(&scratch, raw); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(raw)); err != nil {
		return fmt.Errorf("write setting %s: %w", key, err)
	}
	payload, _ := json.Marshal(map[string][]string{"keys": {key}})
	if _, err := events.Append(tx, events.Event{Type: events.TypeSettingsUpdated, Payload: payload}); err != nil {
		return err
	}
	detail := map[string]any{"key": key, "value": json.RawMessage(raw)}
	if _, err := audit.Write(tx, actor.Entry(audit.ActionSettingsUpdated, "setting", key, detail)); err != nil {
		return err
	}
	tx.AfterCommit(s.Invalidate)
	return nil
}
