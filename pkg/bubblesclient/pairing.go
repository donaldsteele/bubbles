package bubblesclient

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

const maxCABytes = 64 << 10

// FetchCA downloads the CA certificate from the plain-HTTP onboarding
// listener (GET /ca.crt) and returns its DER and its fingerprint
// ("sha256:<64 hex>"). The download itself is unauthenticated: show
// FormatFingerprint(fingerprint) to the user, who must compare it with the
// server's admin UI before trusting it.
func FetchCA(ctx context.Context, onboardingURL string) (der []byte, fingerprint string, err error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(onboardingURL), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, "", errors.New("bubblesclient: onboarding URL must be http(s)://host:port")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String()+"/ca.crt", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	hc := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := hc.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("bubblesclient: fetch CA: HTTP %d", res.StatusCode)
	}
	der, err = io.ReadAll(io.LimitReader(res.Body, maxCABytes))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil || !cert.IsCA {
		return nil, "", errors.New("bubblesclient: the server did not return a CA certificate")
	}
	sum := sha256.Sum256(der)
	return der, "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Pairing is a pending pairing request.
type Pairing struct {
	c       *Client
	id      string
	secret  string
	code    string
	expires time.Time
	cfg     Config
	poll    time.Duration // initial poll interval (tests)

	mu       sync.Mutex
	channels []protocol.PairingChannel // from the claim answer; guarded by mu
}

// RequestPairing asks the admin for a token. serverURL is the HTTPS base
// URL; caFingerprint pins the CA (use the fingerprint the user confirmed
// after FetchCA). Show Code() to the user, then call Wait. An app that was
// paired before sets req.ReplacesAppID to its app id (Client.Self) so the
// admin can reconnect that app, keeping its bot and channels, instead of
// creating a second one; the claim then revokes its old tokens. A server
// that predates the field (bubbles v0.2.0 and earlier) refuses it as an
// unknown field; RequestPairing then sends the request once more without it,
// and that server pairs the app as a new one, as it always did.
func RequestPairing(ctx context.Context, serverURL, caFingerprint string, req protocol.PairingRequest) (*Pairing, error) {
	cfg := Config{BaseURL: serverURL, CAFingerprint: caFingerprint}
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}
	var created protocol.PairingCreated
	err = c.do(ctx, http.MethodPost, apiPrefix+"/pairing/requests", req, &created)
	if req.ReplacesAppID != "" && unknownField(err, "replaces_app_id") {
		req.ReplacesAppID = ""
		err = c.do(ctx, http.MethodPost, apiPrefix+"/pairing/requests", req, &created)
	}
	if err != nil {
		return nil, err
	}
	p := &Pairing{c: c, id: created.ID, secret: created.PollSecret, code: created.Code, cfg: c.Config(), poll: time.Second}
	if t, err := time.Parse(time.RFC3339Nano, created.ExpiresAt); err == nil {
		p.expires = t
	}
	return p, nil
}

// unknownField reports whether err is the 400 a server answers to a request
// body with a field it does not know (details.fields names it "unknown
// field").
func unknownField(err error, field string) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusBadRequest && ae.FieldErrors()[field] == "unknown field"
}

// ID is the pairing request id.
func (p *Pairing) ID() string { return p.id }

// Code is the short code to show the user; the admin compares it.
func (p *Pairing) Code() string { return p.code }

// ExpiresAt is when the request lapses (zero if unknown).
func (p *Pairing) ExpiresAt() time.Time { return p.expires }

// Channels returns the channels the app's bot was in when the token was
// claimed (public and private, not archived, sorted by name), as reported in
// the answer that carried the token. It is nil before Wait succeeded, and
// also when the bot is in no channel: an app that posts alerts should then
// tell the user that an admin must add its bot to a channel. It is safe to
// call from any goroutine, also while Wait runs.
func (p *Pairing) Channels() []protocol.PairingChannel {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.channels)
}

// Wait polls with backoff until the request is approved, denied, expired or
// cancelled, or ctx ends. On approval it returns a Config carrying the token
// and the pinned fingerprint; the server hands the token out exactly once.
// Channels then reports the channels its bot is in. A cancelled request
// (see Cancel) is ErrPairingCancelled.
func (p *Pairing) Wait(ctx context.Context) (Config, error) {
	wait := p.poll
	for {
		if !sleepCtx(ctx, wait) {
			return Config{}, ctx.Err()
		}
		var st protocol.PairingStatus
		err := p.pollOnce(ctx, &st)
		switch {
		case err == nil:
			// The server mints the token on the first poll after approval and
			// reports it once, together with state "claimed".
			if st.Token != "" {
				cfg := p.cfg
				cfg.Token = st.Token
				p.mu.Lock()
				p.channels = st.Channels
				p.mu.Unlock()
				return cfg, nil
			}
			switch st.State {
			case protocol.PairingDenied:
				return Config{}, ErrPairingDenied
			case protocol.PairingExpired:
				return Config{}, ErrPairingExpired
			case protocol.PairingClaimed:
				return Config{}, ErrPairingClaimed
			case protocol.PairingCancelled:
				return Config{}, ErrPairingCancelled
			}
			wait = p.poll
			continue
		case ctx.Err() != nil:
			return Config{}, ctx.Err()
		case errors.Is(err, ErrServerIdentityChanged):
			return Config{}, err
		case retryable(err):
			var ae *APIError
			wait = min(wait*2, 10*time.Second)
			if errors.As(err, &ae) && ae.RetryAfter > 0 {
				wait = ae.RetryAfter
			}
		default:
			return Config{}, err
		}
		if !p.expires.IsZero() && time.Now().After(p.expires.Add(time.Minute)) {
			return Config{}, ErrPairingExpired
		}
	}
}

// Cancel withdraws the request, for a Cancel button while the user waits
// for the admin. A pending request, or an approved one whose token was not
// claimed yet, is cancelled: the admin can no longer approve it, and a Wait
// still running returns ErrPairingCancelled. Cancelling twice is fine. A
// request that already delivered its token, or was denied or expired,
// answers an *APIError with status 409. A server that predates Cancel
// (bubbles v0.2.0 and earlier) answers an *APIError with status 404 or 405;
// its request then simply expires.
func (p *Pairing) Cancel(ctx context.Context) error {
	req, err := p.request(ctx, http.MethodDelete)
	if err != nil {
		return err
	}
	return p.c.roundTrip(req, nil)
}

func (p *Pairing) pollOnce(ctx context.Context, out *protocol.PairingStatus) error {
	req, err := p.request(ctx, http.MethodGet)
	if err != nil {
		return err
	}
	return p.c.roundTrip(req, out)
}

// request builds a call on the request authenticated by the poll secret
// alone.
func (p *Pairing) request(ctx context.Context, method string) (*http.Request, error) {
	req, err := p.c.newRequest(ctx, method, apiPrefix+"/pairing/requests/"+pathEscape(p.id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Del("Authorization")
	req.Header.Set(protocol.PairingHeader, p.secret)
	return req, nil
}
