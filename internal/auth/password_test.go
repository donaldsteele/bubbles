package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHashVerifyCheapParams(t *testing.T) {
	h := NewHasher(cheapParams, 0)
	ctx := context.Background()
	enc, err := h.Hash(ctx, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=8,t=1,p=1$") {
		t.Errorf("hash = %q", enc)
	}
	enc2, _ := h.Hash(ctx, goodPassword)
	if enc == enc2 {
		t.Error("two hashes of the same password share a salt")
	}
	for _, tc := range []struct {
		pw   string
		want bool
	}{{goodPassword, true}, {goodPassword + "x", false}, {"", false}, {strings.ToUpper(goodPassword), false}} {
		got, err := h.Verify(ctx, tc.pw, enc)
		if err != nil || got != tc.want {
			t.Errorf("Verify(%q) = %v, %v; want %v", tc.pw, got, err, tc.want)
		}
	}
	if h.NeedsRehash(enc) {
		t.Error("NeedsRehash on a fresh hash")
	}
	if !NewHasher(Params{Time: 2, MemoryKiB: 8, Threads: 1, SaltLen: 16, KeyLen: 32}, 0).NeedsRehash(enc) {
		t.Error("NeedsRehash ignores changed parameters")
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	h := NewHasher(cheapParams, 0)
	for _, enc := range []string{
		"", "plain", "$argon2i$v=19$m=8,t=1,p=1$c2FsdHNhbHRzYWx0$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=18$m=8,t=1,p=1$c2FsdHNhbHRzYWx0$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=8,t=1$c2FsdHNhbHRzYWx0$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHRzYWx0$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=8,t=0,p=1$c2FsdHNhbHRzYWx0$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=8,t=1,p=1$!!!$AAAAAAAAAAAAAAAAAAAAAA",
	} {
		if ok, err := h.Verify(context.Background(), "x", enc); ok || !errors.Is(err, ErrInvalidHash) {
			t.Errorf("Verify(%q) = %v, %v; want ErrInvalidHash", enc, ok, err)
		}
		if !h.NeedsRehash(enc) {
			t.Errorf("NeedsRehash(%q) = false", enc)
		}
	}
}

// TestRealParams runs argon2id once with the production parameters.
func TestRealParams(t *testing.T) {
	h := NewHasher(DefaultParams, 0)
	ctx := context.Background()
	enc, err := h.Hash(ctx, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("hash = %q", enc)
	}
	d, err := decodeHash(enc)
	if err != nil || len(d.salt) != 16 || len(d.key) != 32 {
		t.Fatalf("decode: %+v, %v", d, err)
	}
	if ok, err := h.Verify(ctx, goodPassword, enc); !ok || err != nil {
		t.Errorf("Verify = %v, %v", ok, err)
	}
	if ok, _ := h.Verify(ctx, "wrong password!", enc); ok {
		t.Error("wrong password verified")
	}
}

func TestHasherQueuesAndHonorsContext(t *testing.T) {
	h := NewHasher(cheapParams, 1)
	h.sem <- struct{}{} // the only slot is busy
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("Hash = %v, want context.Canceled", err)
	}
	if _, err := h.Verify(ctx, "x", "$argon2id$v=19$m=8,t=1,p=1$c2FsdHNhbHRzYWx0$AAAAAAAAAAAAAAAAAAAAAA"); !errors.Is(err, context.Canceled) {
		t.Errorf("Verify = %v, want context.Canceled", err)
	}
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name, pw, want string
	}{
		{"ok", goodPassword, ""},
		{"one character", "a", ""},
		{"common", "password1", ""},
		{"short multibyte", "é", ""},
		{"empty", "", "is required"},
		{"128 runes", strings.Repeat("a", 128), ""},
		{"129 runes", strings.Repeat("a", 129), "at most 128"},
	}
	for _, tt := range tests {
		err := ValidatePassword(tt.pw)
		if tt.want == "" {
			if err != nil {
				t.Errorf("%s: %v", tt.name, err)
			}
			continue
		}
		var pe *PolicyError
		if !errors.As(err, &pe) || !strings.Contains(pe.Reason, tt.want) {
			t.Errorf("%s: err = %v, want reason containing %q", tt.name, err, tt.want)
		}
	}
}

func TestMaybeFreeOSMemoryIsDebounced(t *testing.T) {
	h := NewHasher(cheapParams, 0)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		at      time.Duration // offset from t0
		wantRun bool
	}{
		{"first call runs", 0, true},
		{"inside the 30 s window is skipped", 10 * time.Second, false},
		{"just under the window is skipped", freeOSMemoryInterval - time.Nanosecond, false},
		{"after the window runs again", freeOSMemoryInterval, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := h.lastFree.Load()
			now := t0.Add(tc.at)
			h.maybeFreeOSMemory(now)
			after := h.lastFree.Load()
			if ran := after != before; ran != tc.wantRun {
				t.Fatalf("ran = %v, want %v", ran, tc.wantRun)
			}
			if tc.wantRun && after != now.UnixNano() {
				t.Fatalf("lastFree = %d, want %d", after, now.UnixNano())
			}
		})
	}
}
