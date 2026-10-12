package settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/store"
)

func openStore(t *testing.T) (*Store, *store.DB) {
	t.Helper()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName),
		store.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db), db
}

func count(t *testing.T, db *store.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Read().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDefaults(t *testing.T) {
	s, _ := openStore(t)
	if got := s.Values(); !reflect.DeepEqual(got, Defaults()) {
		t.Errorf("fresh values = %+v", got)
	}
	checks := map[string]bool{
		"instance_name bubbles":  s.InstanceName() == "bubbles",
		"open_registration true": s.RegistrationOpen(time.Now()),
		"window 24h":             s.Values().RegistrationWindowH == 24 && s.RegistrationClosesAt() == nil,
		"upload_max 100 MiB":     s.UploadMaxBytes() == 100<<20,
		"quota 5 GiB":            s.UserQuotaBytes() == 5<<30,
		"reserve 1 GiB":          s.DiskReserveBytes() == 1<<30,
		"retention forever":      s.DefaultRetentionDays() == nil,
		"web push off":           !s.WebPushEnabled(),
		"push payload minimal":   s.PushPayload() == PushMinimal,
		"allowed_cidrs empty":    len(s.AllowedCIDRs()) == 0,
		"session idle 30 days":   s.SessionIdleDays() == 30,
		"alert renotify 1800":    s.AlertRenotifyS() == 1800,
		"mdns on":                s.MDNSEnabled(),
		"web reset enabled":      s.WebResetEnabled(),
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("default %s", name)
		}
	}
	if got := len(Keys()); got != 15 {
		t.Errorf("%d keys, want 15", got)
	}
}

func TestSetUpdatesCacheEventAndAudit(t *testing.T) {
	s, db := openStore(t)
	ctx := context.Background()
	if s.InstanceName() != "bubbles" { // primes the cache
		t.Fatal("default")
	}
	actor := audit.User("u1", "192.168.1.9")
	if err := s.Set(ctx, actor, KeyInstanceName, "  Home Chat "); err != nil {
		t.Fatal(err)
	}
	if got := s.InstanceName(); got != "Home Chat" {
		t.Errorf("InstanceName = %q (cache not invalidated?)", got)
	}
	if err := s.Set(ctx, actor, KeyAllowedCIDRs, []string{"203.0.113.77/24", "2001:db8::/32"}); err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8::/32")}
	if got := s.AllowedCIDRs(); !reflect.DeepEqual(got, want) {
		t.Errorf("AllowedCIDRs = %v, want %v", got, want)
	}
	if err := s.Set(ctx, actor, KeyDefaultRetentionDays, 90); err != nil {
		t.Fatal(err)
	}
	if p := s.DefaultRetentionDays(); p == nil || *p != 90 {
		t.Errorf("DefaultRetentionDays = %v", p)
	}
	if err := s.Set(ctx, actor, KeyDefaultRetentionDays, nil); err != nil {
		t.Fatal(err)
	}
	if s.DefaultRetentionDays() != nil {
		t.Error("retention not reset to forever")
	}

	if n := count(t, db, `SELECT COUNT(*) FROM events WHERE type = 'settings.updated'`); n != 4 {
		t.Errorf("%d settings.updated events, want 4", n)
	}
	var payload string
	if err := db.Read().QueryRow(`SELECT payload FROM events ORDER BY seq LIMIT 1`).Scan(&payload); err != nil || payload != `{"keys":["instance_name"]}` {
		t.Errorf("event payload = %q, %v (values must not leak into events)", payload, err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log WHERE action = 'settings.update' AND actor_id = 'u1' AND actor_ip = '192.168.1.9' AND target_id = 'instance_name'`); n != 1 {
		t.Errorf("audit rows for instance_name = %d", n)
	}
}

func TestSetRejectsInvalid(t *testing.T) {
	s, db := openStore(t)
	ctx := context.Background()
	tests := []struct {
		key   string
		value any
	}{
		{"nope", 1},
		{KeyInstanceName, ""},
		{KeyInstanceName, "   "},
		{KeyInstanceName, "bad\nname"},
		{KeyInstanceName, string(make([]byte, 65))},
		{KeyInstanceName, 5},
		{KeyOpenRegistration, "yes"},
		{KeyUploadMaxBytes, 0},
		{KeyUploadMaxBytes, MaxUploadBytes + 1},
		{KeyUploadMaxBytes, 1.5},
		{KeyUserQuotaBytes, -1},
		{KeyDiskReserveBytes, -5},
		{KeyDefaultRetentionDays, 0},
		{KeyPushPayload, "everything"},
		{KeyAllowedCIDRs, []string{"not a cidr"}},
		{KeyAllowedCIDRs, "10.0.0.0/8"},
		{KeySessionIdleDays, 0},
		{KeySessionIdleDays, 91},
		{KeyAlertRenotifyS, 1},
	}
	for _, tt := range tests {
		err := s.Set(ctx, audit.System, tt.key, tt.value)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("Set(%s, %v) = %v, want ErrInvalid", tt.key, tt.value, err)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM settings`) + count(t, db, `SELECT COUNT(*) FROM events`) + count(t, db, `SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Errorf("%d rows written by rejected values", n)
	}
	if err := Validate(KeySessionIdleDays, 7); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestSetTxRollsBackWithCaller(t *testing.T) {
	s, db := openStore(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := db.Do(ctx, func(tx *store.Tx) error {
		if err := s.SetTx(tx, audit.System, KeyInstanceName, "Ghost"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if s.InstanceName() != "bubbles" {
		t.Error("rolled-back value visible")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Errorf("%d audit rows after rollback", n)
	}
}

func TestInvalidStoredRowFallsBackToDefault(t *testing.T) {
	s, db := openStore(t)
	err := db.Do(context.Background(), func(tx *store.Tx) error {
		for k, v := range map[string]string{KeySessionIdleDays: "9999", KeyMDNSEnabled: `"maybe"`, "unknown_key": "1", KeyUploadMaxBytes: "2048"} {
			if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, k, v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	v := s.Values()
	if v.SessionIdleDays != 30 || !v.MDNSEnabled || v.UploadMaxBytes != 2048 {
		t.Errorf("values = %+v", v)
	}
}

func TestInvalidateAfterExternalChange(t *testing.T) {
	s, db := openStore(t)
	if s.SessionIdleDays() != 30 {
		t.Fatal("default")
	}
	err := db.Do(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('session_idle_days', '14')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionIdleDays() != 30 {
		t.Error("cache was bypassed")
	}
	s.Invalidate()
	if s.SessionIdleDays() != 14 {
		t.Error("Invalidate did not reload")
	}
}

func TestRegistrationOpenWindow(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	until := base.Add(24 * time.Hour)
	if err := s.Set(ctx, audit.Console, KeyOpenRegistrationEnds, DeadlineValue(&until)); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		at   time.Time
		want bool
	}{
		"before": {until.Add(-time.Millisecond), true},
		"at":     {until, false},
		"after":  {until.Add(time.Millisecond), false},
	} {
		if got := s.RegistrationOpen(tc.at); got != tc.want {
			t.Errorf("%s the deadline: open = %v, want %v", name, got, tc.want)
		}
	}
	if got := s.RegistrationClosesAt(); got == nil || !got.Equal(until) {
		t.Errorf("closes at = %v, want %v", got, until)
	}
	// A fresh store over the same database still says closed.
	if New(s.db).RegistrationOpen(until.Add(time.Hour)) {
		t.Error("a new store reopened sign-up after the deadline")
	}
	if err := s.Set(ctx, audit.Console, KeyOpenRegistrationEnds, nil); err != nil {
		t.Fatal(err)
	}
	if !s.RegistrationOpen(until.Add(1000 * time.Hour)) {
		t.Error("no deadline should stay open")
	}
	if err := s.Set(ctx, audit.Console, KeyOpenRegistration, false); err != nil {
		t.Fatal(err)
	}
	if s.RegistrationOpen(base) || s.RegistrationClosesAt() != nil {
		t.Error("switched off should be closed with no deadline")
	}
}

func TestRegistrationSettingsValidation(t *testing.T) {
	for _, tc := range []struct {
		key string
		val any
		ok  bool
	}{
		{KeyRegistrationWindowH, 0, true},
		{KeyRegistrationWindowH, 8760, true},
		{KeyRegistrationWindowH, 8761, false},
		{KeyRegistrationWindowH, -1, false},
		{KeyOpenRegistrationEnds, nil, true},
		{KeyOpenRegistrationEnds, "2026-01-02T03:04:05.000Z", true},
		{KeyOpenRegistrationEnds, "tomorrow", false},
		{KeyOpenRegistrationEnds, 5, false},
	} {
		err := Validate(tc.key, tc.val)
		if (err == nil) != tc.ok {
			t.Errorf("Validate(%s, %v) = %v, ok want %v", tc.key, tc.val, err, tc.ok)
		}
	}
}

func TestSetManyAtDeadlineRules(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	patch := func(kv map[string]any) {
		t.Helper()
		m := map[string]json.RawMessage{}
		for k, v := range kv {
			m[k], _ = json.Marshal(v)
		}
		if err := s.SetManyAt(ctx, audit.Console, now, m); err != nil {
			t.Fatal(err)
		}
	}
	patch(map[string]any{KeyOpenRegistration: false})
	if s.RegistrationClosesAt() != nil || s.RegistrationOpen(now) {
		t.Fatal("off should be closed")
	}
	patch(map[string]any{KeyOpenRegistration: true})
	if got := s.RegistrationClosesAt(); got == nil || !got.Equal(now.Add(24*time.Hour)) {
		t.Errorf("switch on: closes at %v", got)
	}
	later := now.Add(5 * time.Hour)
	if err := s.SetManyAt(ctx, audit.Console, later, map[string]json.RawMessage{KeyRegistrationWindowH: json.RawMessage("2")}); err != nil {
		t.Fatal(err)
	}
	if got := s.RegistrationClosesAt(); got == nil || !got.Equal(later.Add(2*time.Hour)) {
		t.Errorf("window change: closes at %v", got)
	}
	patch(map[string]any{KeyRegistrationWindowH: 0})
	if s.RegistrationClosesAt() != nil || !s.RegistrationOpen(now.Add(9999*time.Hour)) {
		t.Error("window 0 should clear the deadline")
	}
	patch(map[string]any{KeyOpenRegistration: false})
	patch(map[string]any{KeyRegistrationWindowH: 6})
	if s.RegistrationOpen(now) {
		t.Error("changing the window must not open sign-up")
	}
	if err := s.ReopenRegistration(ctx, audit.Console, now); err != nil {
		t.Fatal(err)
	}
	if got := s.RegistrationClosesAt(); got == nil || !got.Equal(now.Add(6*time.Hour)) {
		t.Errorf("reopen: closes at %v", got)
	}
	if err := s.CloseRegistration(ctx, audit.Console); err != nil {
		t.Fatal(err)
	}
	if s.RegistrationOpen(now) || s.RegistrationClosesAt() != nil {
		t.Error("close")
	}
}
