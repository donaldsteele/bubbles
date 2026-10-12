package settings

import (
	"context"
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
		"instance_name bubbles":   s.InstanceName() == "bubbles",
		"open_registration false": !s.OpenRegistration(),
		"upload_max 100 MiB":      s.UploadMaxBytes() == 100<<20,
		"quota 5 GiB":             s.UserQuotaBytes() == 5<<30,
		"reserve 1 GiB":           s.DiskReserveBytes() == 1<<30,
		"retention forever":       s.DefaultRetentionDays() == nil,
		"web push off":            !s.WebPushEnabled(),
		"push payload minimal":    s.PushPayload() == PushMinimal,
		"allowed_cidrs empty":     len(s.AllowedCIDRs()) == 0,
		"session idle 30 days":    s.SessionIdleDays() == 30,
		"alert renotify 1800":     s.AlertRenotifyS() == 1800,
		"mdns on":                 s.MDNSEnabled(),
		"web reset enabled":       s.WebResetEnabled(),
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("default %s", name)
		}
	}
	if got := len(Keys()); got != 13 {
		t.Errorf("%d keys, want 13", got)
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
