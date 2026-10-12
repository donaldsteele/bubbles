package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// MaxPasswordLen is the longest accepted password, in characters (runes).
const MaxPasswordLen = 128

// Params are argon2id parameters.
type Params struct {
	Time      uint32 // passes
	MemoryKiB uint32
	Threads   uint8
	SaltLen   int
	KeyLen    uint32
}

// DefaultParams are the production parameters (Locked Decision 2):
// t=3, m=64 MiB, p=4, 16-byte salt, 32-byte key.
var DefaultParams = Params{Time: 3, MemoryKiB: 64 * 1024, Threads: 4, SaltLen: 16, KeyLen: 32}

// DefaultMaxConcurrentHashes bounds simultaneous argon2 computations: each
// holds MemoryKiB of memory, so a login burst must queue, not allocate.
const DefaultMaxConcurrentHashes = 4

// Limits on the parameters read back from a stored hash, so a corrupt row
// cannot make Verify allocate gigabytes.
const (
	maxStoredMemoryKiB = 1 << 20
	maxStoredTime      = 32
	maxStoredThreads   = 64
)

// ErrInvalidHash means a stored password hash is not a supported PHC string.
var ErrInvalidHash = errors.New("invalid password hash")

// PolicyError is returned by ValidatePassword; Reason is safe to show.
type PolicyError struct{ Reason string }

func (e *PolicyError) Error() string { return "password " + e.Reason }

// ValidatePassword enforces the password policy: not empty and at most 128 characters.
func ValidatePassword(pw string) error {
	if pw == "" {
		return &PolicyError{"is required"}
	}
	if n := utf8.RuneCountInString(pw); n > MaxPasswordLen {
		return &PolicyError{fmt.Sprintf("must be at most %d characters", MaxPasswordLen)}
	}
	return nil
}

// Hasher computes and verifies argon2id hashes in PHC string format.
type Hasher struct {
	params Params
	sem    chan struct{}
	// lastFree is the UnixNano time of the last debug.FreeOSMemory call.
	lastFree atomic.Int64
}

// freeOSMemoryInterval bounds how often a login burst can trigger
// debug.FreeOSMemory, which is a full GC plus scavenge.
const freeOSMemoryInterval = 30 * time.Second

// NewHasher returns a Hasher that creates hashes with p. At most
// maxConcurrent computations run at once (0 means DefaultMaxConcurrentHashes).
func NewHasher(p Params, maxConcurrent int) *Hasher {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrentHashes
	}
	return &Hasher{params: p, sem: make(chan struct{}, maxConcurrent)}
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release frees a slot. When the last running hash finishes, the argon2
// working memory (64 MiB each) is handed back to the OS so a login burst does
// not leave the process bloated for the rest of its life.
func (h *Hasher) release() {
	<-h.sem
	if len(h.sem) == 0 {
		h.maybeFreeOSMemory(time.Now())
	}
}

// maybeFreeOSMemory runs debug.FreeOSMemory in the background unless one ran
// less than freeOSMemoryInterval before now. The compare-and-swap keeps
// concurrent releases from starting more than one call per window.
func (h *Hasher) maybeFreeOSMemory(now time.Time) {
	last := h.lastFree.Load()
	n := now.UnixNano()
	if last != 0 && n-last < int64(freeOSMemoryInterval) {
		return
	}
	if !h.lastFree.CompareAndSwap(last, n) {
		return
	}
	go debug.FreeOSMemory()
}

var rawB64 = base64.RawStdEncoding

// Hash returns the PHC string of password with a fresh random salt:
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, h.params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	key := argon2.IDKey([]byte(password), salt, h.params.Time, h.params.MemoryKiB, h.params.Threads, h.params.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		h.params.MemoryKiB, h.params.Time, h.params.Threads, rawB64.EncodeToString(salt), rawB64.EncodeToString(key)), nil
}

// decoded is a parsed PHC string.
type decoded struct {
	params Params
	salt   []byte
	key    []byte
}

func decodeHash(encoded string) (decoded, error) {
	var d decoded
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || !strings.HasPrefix(parts[2], "v=") {
		return d, ErrInvalidHash
	}
	if v, err := strconv.Atoi(parts[2][2:]); err != nil || v != argon2.Version {
		return d, ErrInvalidHash
	}
	var m, t, p uint64
	for _, kv := range strings.Split(parts[3], ",") {
		k, val, ok := strings.Cut(kv, "=")
		if !ok {
			return d, ErrInvalidHash
		}
		n, err := strconv.ParseUint(val, 10, 32)
		if err != nil {
			return d, ErrInvalidHash
		}
		switch k {
		case "m":
			m = n
		case "t":
			t = n
		case "p":
			p = n
		default:
			return d, ErrInvalidHash
		}
	}
	if m == 0 || m > maxStoredMemoryKiB || t == 0 || t > maxStoredTime || p == 0 || p > maxStoredThreads {
		return d, ErrInvalidHash
	}
	salt, err := rawB64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return d, ErrInvalidHash
	}
	key, err := rawB64.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 128 {
		return d, ErrInvalidHash
	}
	d.params = Params{Time: uint32(t), MemoryKiB: uint32(m), Threads: uint8(p), SaltLen: len(salt), KeyLen: uint32(len(key))}
	d.salt, d.key = salt, key
	return d, nil
}

// Verify reports whether password matches encoded, comparing in constant
// time with the parameters stored in encoded.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	d, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	key := argon2.IDKey([]byte(password), d.salt, d.params.Time, d.params.MemoryKiB, d.params.Threads, d.params.KeyLen)
	return subtle.ConstantTimeCompare(key, d.key) == 1, nil
}

// NeedsRehash reports whether encoded was made with different parameters
// than this Hasher creates (or cannot be parsed).
func (h *Hasher) NeedsRehash(encoded string) bool {
	d, err := decodeHash(encoded)
	return err != nil || d.params != h.params
}
