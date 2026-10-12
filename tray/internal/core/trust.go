package core

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
)

// TrustPhase is where the CA trust flow stands.
type TrustPhase int

const (
	// TrustNone: no CA is pinned and none is being confirmed.
	TrustNone TrustPhase = iota
	// TrustPending: a CA was downloaded; the user must confirm its fingerprint.
	TrustPending
	// TrustPinned: the user confirmed a CA; it is pinned.
	TrustPinned
	// TrustChanged: the pinned CA no longer matches the server. Nothing is
	// trusted until the user confirms the new fingerprint (TrustPending with
	// Changed set).
	TrustChanged
)

// Errors of the trust flow.
var (
	// ErrNothingToConfirm: Confirm was called with no pending fingerprint.
	ErrNothingToConfirm = errors.New("there is no fingerprint waiting for confirmation")
	// ErrFingerprintMismatch: the confirmed fingerprint is not the one shown.
	ErrFingerprintMismatch = errors.New("the confirmed fingerprint does not match the one shown")
	// ErrBadFingerprint: the text is not a usable SHA-256 fingerprint.
	ErrBadFingerprint = errors.New("invalid fingerprint")
)

// Trust is the pairing/trust state machine. It never pins a fingerprint the
// user has not confirmed, and a changed fingerprint always goes through a
// new confirmation: there is no silent re-trust path.
type Trust struct {
	phase   TrustPhase
	pinned  string // normalized "sha256:<hex>"
	pending string
	changed bool
	// broken is set by IdentityChanged and cleared only by Confirm or
	// Forget, so no path other than a user confirmation re-trusts the pin.
	broken bool
}

// NewTrust starts from a stored pin ("" = none).
func NewTrust(pinned string) (*Trust, error) {
	t := &Trust{}
	if pinned == "" {
		return t, nil
	}
	fp, err := NormalizeFingerprint(pinned)
	if err != nil {
		return nil, err
	}
	t.phase, t.pinned = TrustPinned, fp
	return t, nil
}

// Phase returns the current phase.
func (t *Trust) Phase() TrustPhase { return t.phase }

// Pinned returns the pinned fingerprint, or "".
func (t *Trust) Pinned() string { return t.pinned }

// Prompt describes what the user is asked to confirm.
type Prompt struct {
	// Fingerprint is the normalized "sha256:<hex>" value.
	Fingerprint string `json:"fingerprint"`
	// Grouped is the fingerprint in groups of four for comparing by eye.
	Grouped string `json:"grouped"`
	// Changed is true when this replaces an already pinned CA.
	Changed bool `json:"changed"`
	// Previous is the grouped fingerprint that was pinned, when Changed.
	Previous string `json:"previous,omitempty"`
}

// Propose records a downloaded CA fingerprint for confirmation. If a
// different CA is already pinned, the prompt is flagged as a change.
func (t *Trust) Propose(fp string) (Prompt, error) {
	n, err := NormalizeFingerprint(fp)
	if err != nil {
		return Prompt{}, err
	}
	t.pending = n
	t.changed = t.pinned != "" && t.pinned != n
	p := Prompt{Fingerprint: n, Grouped: GroupFingerprint(n), Changed: t.changed}
	if t.changed {
		p.Previous = GroupFingerprint(t.pinned)
	}
	if t.pinned == "" || t.changed || t.broken {
		t.phase = TrustPending
	}
	return p, nil
}

// IdentityChanged is called when the server's certificate chain no longer
// ends in the pinned CA (bubblesclient.ErrServerIdentityChanged). The pin is
// kept but marked unusable until the user confirms a new fingerprint.
func (t *Trust) IdentityChanged() {
	if t.pinned != "" {
		t.phase, t.broken = TrustChanged, true
	}
	t.pending = ""
}

// Confirm pins the fingerprint the user confirmed. It must equal the one
// that was proposed (what the user was shown); anything else is refused.
func (t *Trust) Confirm(shown string) (string, error) {
	if t.pending == "" {
		return "", ErrNothingToConfirm
	}
	n, err := NormalizeFingerprint(shown)
	if err != nil {
		return "", err
	}
	if subtle.ConstantTimeCompare([]byte(n), []byte(t.pending)) != 1 {
		return "", ErrFingerprintMismatch
	}
	t.pinned, t.pending, t.changed, t.broken, t.phase = n, "", false, false, TrustPinned
	return n, nil
}

// Reject drops the pending fingerprint. A previous pin stays but, after an
// identity change, remains unusable.
func (t *Trust) Reject() {
	t.pending, t.changed = "", false
	switch {
	case t.broken:
		t.phase = TrustChanged
	case t.pinned != "":
		t.phase = TrustPinned
	default:
		t.phase = TrustNone
	}
}

// Forget removes the pin (sign-out).
func (t *Trust) Forget() { *t = Trust{} }

// Usable reports whether the pinned CA may be used to talk to the server.
func (t *Trust) Usable() bool { return t.phase == TrustPinned }

// NormalizeFingerprint accepts "sha256:<hex>", bare hex, or hex grouped with
// spaces, colons or dashes, and returns "sha256:<64 lower-case hex>".
func NormalizeFingerprint(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "sha256:")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
			b.WriteRune(r)
		case r == ' ' || r == ':' || r == '-':
		default:
			return "", ErrBadFingerprint
		}
	}
	if b.Len() != 64 {
		return "", ErrBadFingerprint
	}
	return "sha256:" + b.String(), nil
}

// GroupFingerprint formats a fingerprint as groups of four hex digits.
func GroupFingerprint(fp string) string {
	h := strings.TrimPrefix(strings.ToLower(fp), "sha256:")
	var parts []string
	for len(h) > 4 {
		parts = append(parts, h[:4])
		h = h[4:]
	}
	if h != "" {
		parts = append(parts, h)
	}
	return strings.Join(parts, " ")
}

// FingerprintOf returns the "sha256:<hex>" of DER data.
func FingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}
