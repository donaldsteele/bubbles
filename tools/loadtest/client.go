package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

const cookieName = "__Host-bubbles_session"

// api is a minimal cookie-session REST client.
type api struct {
	base string // https://host:port
	http *http.Client
}

func newAPI(base string, pool *x509.CertPool, insecure bool) *api {
	tr := &http.Transport{
		TLSClientConfig:     &tls.Config{RootCAs: pool, InsecureSkipVerify: insecure, MinVersion: tls.VersionTLS12}, //nolint:gosec // opt-in flag for ad-hoc targets
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
	}
	return &api{base: strings.TrimRight(base, "/"), http: &http.Client{Transport: tr, Timeout: 30 * time.Second}}
}

// do sends a JSON request; cookie may be empty. It returns the status, body
// and the session cookie value set by the response (if any).
func (a *api) do(ctx context.Context, method, path, cookie string, in any) (int, []byte, string, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, nil, "", err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, body)
	if err != nil {
		return 0, nil, "", err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", a.base)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	res, err := a.http.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	set := ""
	for _, c := range res.Cookies() {
		if c.Name == cookieName {
			set = c.Value
		}
	}
	return res.StatusCode, data, set, err
}

// call performs a request and decodes the JSON answer into out when the
// status equals want. It returns the session cookie set by the response.
func (a *api) call(ctx context.Context, method, path, cookie string, in, out any, want int) (string, error) {
	status, data, set, err := a.do(ctx, method, path, cookie, in)
	if err != nil {
		return "", err
	}
	if status != want {
		var eb protocol.ErrorBody
		_ = json.Unmarshal(data, &eb)
		return "", fmt.Errorf("%s %s: status %d (%s)", method, path, status, eb.Error.Code)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return "", fmt.Errorf("%s %s: decode: %w", method, path, err)
		}
	}
	return set, nil
}

// randomPassword returns a strong unique password (never a dictionary word).
func randomPassword() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return "Lt!" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

// user is one load-test account with its session.
type user struct {
	name     string
	password string
	id       string
	cookie   string
	channel  int // index into the channel list
}

// ws is one realtime client. It records frames and never prints them.
type ws struct {
	u     *user
	conn  *websocket.Conn
	ready chan struct{}
	once  sync.Once

	mu         sync.Mutex
	lastSeq    int64
	seqRegress int64
	dups       int64
	got        map[string]struct{}
	latencies  []time.Duration
	closeErr   error
}

// bodyPrefix marks load-test messages; the body is "lt|<unix nanos>|<sender>|<n>".
const bodyPrefix = "lt|"

func (a *api) dialWS(ctx context.Context, u *user) (*ws, error) {
	wsURL := "wss" + strings.TrimPrefix(a.base, "https") + protocol.WSPath
	hdr := http.Header{}
	hdr.Set("Origin", a.base)
	hdr.Set("Cookie", cookieName+"="+u.cookie)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: a.http, HTTPHeader: hdr})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(4 << 20)
	return &ws{u: u, conn: conn, ready: make(chan struct{}), got: map[string]struct{}{}}, nil
}

// run reads frames until the connection closes.
func (c *ws) run(ctx context.Context) {
	for {
		_, data, err := c.conn.Read(ctx)
		now := time.Now()
		if err != nil {
			c.mu.Lock()
			c.closeErr = err
			c.mu.Unlock()
			return
		}
		var f struct {
			Type string          `json:"type"`
			Seq  int64           `json:"seq"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		if f.Type == protocol.FrameHello {
			c.once.Do(func() { close(c.ready) })
			continue
		}
		c.mu.Lock()
		// Gaps are expected (see the package comment); only a non-increasing seq counts.
		if f.Seq > 0 {
			if f.Seq <= c.lastSeq {
				c.seqRegress++
			} else {
				c.lastSeq = f.Seq
			}
		}
		if f.Type == "message.created" {
			var m protocol.Message
			if json.Unmarshal(f.Data, &m) == nil && strings.HasPrefix(m.Body, bodyPrefix) {
				if _, dup := c.got[m.ID]; dup {
					c.dups++
				} else {
					c.got[m.ID] = struct{}{}
					if parts := strings.Split(m.Body, "|"); len(parts) >= 2 {
						if ns, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
							c.latencies = append(c.latencies, now.Sub(time.Unix(0, ns)))
						}
					}
				}
			}
		}
		c.mu.Unlock()
	}
}
