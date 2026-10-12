package core

import (
	"errors"
	"strings"
	"testing"
)

var (
	fpA = "sha256:" + strings.Repeat("ab", 32)
	fpB = "sha256:" + strings.Repeat("cd", 32)
)

func TestNormalizeAndGroupFingerprint(t *testing.T) {
	grouped := GroupFingerprint(fpA)
	if !strings.HasPrefix(grouped, "abab abab abab") || len(strings.Fields(grouped)) != 16 {
		t.Fatalf("grouped = %q", grouped)
	}
	for _, in := range []string{fpA, strings.ToUpper(fpA), strings.TrimPrefix(fpA, "sha256:"), grouped, strings.ReplaceAll(grouped, " ", ":")} {
		got, err := NormalizeFingerprint(in)
		if err != nil || got != fpA {
			t.Errorf("NormalizeFingerprint(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "sha256:abcd", fpA + "00", "sha256:" + strings.Repeat("zz", 32)} {
		if _, err := NormalizeFingerprint(bad); !errors.Is(err, ErrBadFingerprint) {
			t.Errorf("NormalizeFingerprint(%q) err = %v", bad, err)
		}
	}
	if got := FingerprintOf([]byte("x")); !strings.HasPrefix(got, "sha256:2d711642b726b044") {
		t.Errorf("FingerprintOf = %q", got)
	}
}

func TestTrustFirstUseRequiresConfirmation(t *testing.T) {
	tr, err := NewTrust("")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Usable() || tr.Phase() != TrustNone {
		t.Fatal("fresh trust is usable")
	}
	if _, err := tr.Confirm(fpA); !errors.Is(err, ErrNothingToConfirm) {
		t.Fatalf("confirm without proposal: %v", err)
	}
	p, err := tr.Propose(strings.ToUpper(fpA))
	if err != nil || p.Fingerprint != fpA || p.Changed || p.Grouped != GroupFingerprint(fpA) {
		t.Fatalf("prompt = %+v, %v", p, err)
	}
	if tr.Phase() != TrustPending || tr.Usable() || tr.Pinned() != "" {
		t.Fatal("proposal pinned without confirmation")
	}
	// Confirming a different fingerprint than the one shown is refused.
	if _, err := tr.Confirm(fpB); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("confirm of another fingerprint: %v", err)
	}
	if tr.Usable() {
		t.Fatal("mismatching confirmation pinned")
	}
	got, err := tr.Confirm(GroupFingerprint(fpA))
	if err != nil || got != fpA || !tr.Usable() || tr.Pinned() != fpA {
		t.Fatalf("confirm = %q, %v", got, err)
	}
	if _, err := tr.Confirm(fpA); !errors.Is(err, ErrNothingToConfirm) {
		t.Fatalf("second confirm: %v", err)
	}
}

func TestTrustIdentityChangeIsNeverSilent(t *testing.T) {
	tr, err := NewTrust(fpA)
	if err != nil || !tr.Usable() {
		t.Fatalf("NewTrust = %v", err)
	}
	tr.IdentityChanged()
	if tr.Usable() || tr.Phase() != TrustChanged {
		t.Fatal("pin still usable after an identity change")
	}
	// The same CA reappearing still needs a fresh confirmation here: the
	// phase only returns to pinned through Confirm.
	p, err := tr.Propose(fpB)
	if err != nil || !p.Changed || p.Previous != GroupFingerprint(fpA) {
		t.Fatalf("prompt = %+v, %v", p, err)
	}
	if tr.Usable() {
		t.Fatal("proposal alone re-trusted the server")
	}
	if _, err := tr.Confirm(fpA); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("confirming the old fingerprint: %v", err)
	}
	tr.Reject()
	if tr.Usable() || tr.Phase() != TrustChanged {
		t.Fatalf("after reject phase = %v", tr.Phase())
	}
	if _, err := tr.Propose(fpB); err != nil {
		t.Fatal(err)
	}
	if got, err := tr.Confirm(fpB); err != nil || got != fpB || !tr.Usable() {
		t.Fatalf("confirm new = %q, %v", got, err)
	}
	tr.Forget()
	if tr.Pinned() != "" || tr.Phase() != TrustNone {
		t.Fatal("Forget kept the pin")
	}
}

func TestTrustProposeSamePinKeepsUsable(t *testing.T) {
	tr, _ := NewTrust(fpA)
	p, err := tr.Propose(fpA)
	if err != nil || p.Changed || !tr.Usable() {
		t.Fatalf("prompt = %+v, %v, usable %v", p, err, tr.Usable())
	}
	if _, err := NewTrust("nonsense"); err == nil {
		t.Fatal("bad stored pin accepted")
	}
}
