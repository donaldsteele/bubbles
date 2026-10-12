package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
)

var testNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// env is a push Service over a temp database with two users.
type env struct {
	t       *testing.T
	ctx     context.Context
	db      *store.DB
	st      *settings.Store
	dataDir string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, store.FileName),
		store.Options{Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &env{t: t, ctx: context.Background(), db: db, st: settings.New(db), dataDir: dir}
	e.addUser("u_alice", "alice")
	e.addUser("u_bob", "bob")
	return e
}

func (e *env) addUser(id, username string) {
	e.t.Helper()
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO users (id, username, display_name, kind, role, created_at) VALUES (?, ?, ?, 'human', 'member', ?)`,
			id, username, username, tx.Now())
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) set(key string, value any) {
	e.t.Helper()
	if err := e.st.Set(e.ctx, audit.System, key, value); err != nil {
		e.t.Fatal(err)
	}
}

// service starts a Service whose HTTP client is client and which accepts any
// endpoint (the production allowlist would reject the test servers).
func (e *env) service(client *http.Client, mods ...func(*Options)) *Service {
	e.t.Helper()
	o := Options{DB: e.db, Settings: e.st, DataDir: e.dataDir, Workers: 2, client: client, allowAnyEndpoint: true}
	for _, m := range mods {
		m(&o)
	}
	s, err := New(o)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(s.Close)
	return s
}

// browser is a simulated push subscriber: the keys a real browser would hold.
type browser struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return &browser{priv: priv, auth: auth}
}

func (b *browser) p256dh() string {
	return base64.RawURLEncoding.EncodeToString(b.priv.PublicKey().Bytes())
}
func (b *browser) authKey() string { return base64.RawURLEncoding.EncodeToString(b.auth) }

// insertSub stores a subscription row directly, bypassing the allowlist.
func (e *env) insertSub(userID, endpoint string, b *browser) string {
	e.t.Helper()
	var id string
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		id = tx.NewID()
		_, err := tx.Exec(`INSERT INTO push_subscriptions (id, user_id, endpoint, p256dh, auth, user_agent, created_at) VALUES (?, ?, ?, ?, ?, 'test', ?)`,
			id, userID, endpoint, b.p256dh(), b.authKey(), tx.Now())
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

type subRow struct {
	exists   bool
	failures int
	lastOK   *int64
}

func (e *env) sub(id string) subRow {
	e.t.Helper()
	var r subRow
	var lastOK *int64
	err := e.db.Read().QueryRow(`SELECT failure_count, last_ok_at FROM push_subscriptions WHERE id = ?`, id).Scan(&r.failures, &lastOK)
	if err == nil {
		r.exists, r.lastOK = true, lastOK
	}
	return r
}

func (e *env) count(userID string) int {
	e.t.Helper()
	var n int
	if err := e.db.Read().QueryRow(`SELECT COUNT(*) FROM push_subscriptions WHERE user_id = ?`, userID).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// received is one request a fake push service saw.
type received struct {
	path   string
	header http.Header
	body   []byte
}

// pushServer is a fake browser push service (TLS, so webpush-go's https
// endpoints work). status is the answer for every request.
type pushServer struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	got    []received
	notify chan struct{}
}

func newPushServer(t *testing.T, status int) *pushServer {
	t.Helper()
	p := &pushServer{status: status, notify: make(chan struct{}, 64)}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 0, 8192)
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			body = append(body, buf[:n]...)
			if err != nil {
				break
			}
		}
		p.mu.Lock()
		p.got = append(p.got, received{path: r.URL.Path, header: r.Header.Clone(), body: body})
		status := p.status
		p.mu.Unlock()
		if status == http.StatusTemporaryRedirect {
			w.Header().Set("Location", "/redirected")
		}
		w.WriteHeader(status)
		select {
		case p.notify <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *pushServer) setStatus(s int) {
	p.mu.Lock()
	p.status = s
	p.mu.Unlock()
}

func (p *pushServer) requests() []received {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]received(nil), p.got...)
}

// wait blocks until the server has seen n requests.
func (p *pushServer) wait(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for len(p.requests()) < n {
		select {
		case <-p.notify:
		case <-deadline:
			t.Fatalf("push service saw %d requests, want %d", len(p.requests()), n)
		}
	}
}

func (p *pushServer) endpoint(path string) string { return p.URL + path }

// decrypt opens an RFC 8291 (aes128gcm, RFC 8188) message with the keys of the
// subscription b, as a browser would.
func decrypt(t *testing.T, b *browser, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("body of %d bytes is shorter than the aes128gcm header", len(body))
	}
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	idLen := int(body[20])
	if len(body) < 21+idLen || rs < 18 {
		t.Fatalf("bad aes128gcm header: rs=%d idlen=%d", rs, idLen)
	}
	asPubBytes := body[21 : 21+idLen]
	ciphertext := body[21+idLen:]
	if uint32(len(ciphertext)) > rs {
		t.Fatalf("ciphertext of %d bytes exceeds the record size %d (single record expected)", len(ciphertext), rs)
	}
	asPub, err := ecdh.P256().NewPublicKey(asPubBytes)
	if err != nil {
		t.Fatalf("sender public key: %v", err)
	}
	secret, err := b.priv.ECDH(asPub)
	if err != nil {
		t.Fatal(err)
	}
	uaPub := b.priv.PublicKey().Bytes()
	prkKey, err := hkdf.Extract(sha256.New, secret, b.auth)
	if err != nil {
		t.Fatal(err)
	}
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPub...), asPubBytes...)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
	if err != nil {
		t.Fatal(err)
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		t.Fatal(err)
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := aes.NewCipher([]byte(cek))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, []byte(nonce), ciphertext, nil)
	if err != nil {
		t.Fatalf("payload does not decrypt with the subscription keys: %v", err)
	}
	// Strip the padding: zeros, then the 0x02 delimiter of the last record.
	i := len(plain)
	for i > 0 && plain[i-1] == 0 {
		i--
	}
	if i == 0 || plain[i-1] != 2 {
		t.Fatalf("missing the last-record delimiter in %x", plain)
	}
	return plain[:i-1]
}
