package push

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
)

// Sender defaults and limits.
const (
	// DefaultWorkers is the size of the send worker pool.
	DefaultWorkers = 4
	// DefaultQueueSize bounds the notifications waiting for a worker; beyond
	// it Send drops (and counts) instead of blocking the dispatcher.
	DefaultQueueSize = 1024
	// RequestTimeout is the deadline of one request to a push service.
	RequestTimeout = 10 * time.Second
	// MaxFailures is the number of consecutive failed deliveries after which a
	// subscription is deleted.
	MaxFailures = 5
	// subscriber is the VAPID contact (RFC 8292 "sub"); webpush-go adds the
	// mailto: scheme. An instance has no operator address, so it is a fixed
	// placeholder in a reserved domain.
	subscriber = "push@bubbles.example.com"
	// maxResponseDrain bounds how much of a push service's answer is read
	// before the connection is released.
	maxResponseDrain = 4 << 10
)

// Options configures New.
type Options struct {
	// DB holds the push_subscriptions table. Required.
	DB *store.DB
	// Settings supplies web_push_enabled and push_payload. Required.
	Settings *settings.Store
	// DataDir is where vapid.json lives. Required.
	DataDir string
	// Logger receives delivery warnings; it never sees endpoints. nil discards.
	Logger *slog.Logger
	// Workers and QueueSize default to DefaultWorkers and DefaultQueueSize.
	Workers   int
	QueueSize int
	// Resolver resolves push service host names; nil means the system
	// resolver. The address guard checks every answer.
	Resolver Resolver

	// client replaces the guarded HTTP client; allowAnyEndpoint skips the
	// endpoint allowlist at send time. Both exist for tests only: they are
	// unexported so nothing outside this package can weaken the guards.
	client           webpush.HTTPClient
	allowAnyEndpoint bool
}

// Service owns the VAPID keys, the subscription table and the send worker
// pool.
type Service struct {
	db       *store.DB
	settings *settings.Store
	logger   *slog.Logger
	vapid    VAPID
	client   webpush.HTTPClient
	// allowAny skips the endpoint allowlist when sending (tests only).
	allowAny bool

	queue   chan job
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	closed  atomic.Bool
	dropped atomic.Int64
}

// job is one notification for one user.
type job struct {
	userID string
	n      Notification
}

// New loads (or creates) the VAPID keys and starts the send workers. Close
// stops them.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Settings == nil || o.DataDir == "" {
		return nil, errors.New("push: DB, Settings and DataDir are required")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Workers <= 0 {
		o.Workers = DefaultWorkers
	}
	if o.QueueSize <= 0 {
		o.QueueSize = DefaultQueueSize
	}
	v, err := LoadOrCreateVAPID(o.DataDir)
	if err != nil {
		return nil, err
	}
	client := o.client
	if client == nil {
		client = newClient(newGuardedDialer(o.Resolver).DialContext, &tls.Config{MinVersion: tls.VersionTLS12}, RequestTimeout)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		db: o.DB, settings: o.Settings, logger: o.Logger, vapid: v, client: client,
		allowAny: o.allowAnyEndpoint,
		queue:    make(chan job, o.QueueSize), ctx: ctx, cancel: cancel,
	}
	for range o.Workers {
		s.wg.Add(1)
		go s.worker()
	}
	return s, nil
}

// Close stops the workers; queued notifications are discarded and requests in
// flight are cancelled. It is idempotent.
func (s *Service) Close() {
	if s.closed.Swap(true) {
		return
	}
	s.cancel()
	s.wg.Wait()
}

// PublicKey is the VAPID public key browsers subscribe with.
func (s *Service) PublicKey() string { return s.vapid.PublicKey }

// Enabled reports the admin setting web_push_enabled.
func (s *Service) Enabled() bool { return s.settings.WebPushEnabled() }

// Dropped is the number of notifications Send discarded because the queue was
// full.
func (s *Service) Dropped() int64 { return s.dropped.Load() }

// Send queues n for every subscription of userID. It never blocks: when the
// queue is full the notification is dropped and counted.
func (s *Service) Send(userID string, n Notification) {
	if s.closed.Load() {
		return
	}
	select {
	case s.queue <- job{userID: userID, n: n}:
	default:
		if c := s.dropped.Add(1); c == 1 || c%1000 == 0 {
			s.logger.Warn("push: send queue full, dropping notifications", "dropped_total", c)
		}
	}
}

func (s *Service) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case j := <-s.queue:
			s.deliver(s.ctx, j)
		}
	}
}

// outcome classifies one delivery attempt.
type outcome int

const (
	outcomeOK outcome = iota
	outcomeGone
	outcomeFailed
)

// deliver sends j to every subscription of its user, one request at a time,
// each bounded by RequestTimeout.
func (s *Service) deliver(ctx context.Context, j job) {
	if !s.settings.WebPushEnabled() {
		return
	}
	subs, err := s.Subscriptions(ctx, j.userID)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("push: list subscriptions", "err", err)
		}
		return
	}
	if len(subs) == 0 {
		return
	}
	payload, err := BuildPayload(j.n, s.settings.PushPayload())
	if err != nil {
		s.logger.Error("push: build payload", "err", err)
		return
	}
	urgency := webpush.UrgencyNormal
	if j.n.Critical {
		urgency = webpush.UrgencyHigh
	}
	for _, sub := range subs {
		if ctx.Err() != nil {
			return
		}
		s.sendOne(ctx, sub, payload, urgency)
	}
}

func (s *Service) sendOne(ctx context.Context, sub Subscription, payload []byte, urgency webpush.Urgency) {
	res, status := s.post(ctx, sub, payload, urgency)
	if ctx.Err() != nil {
		return // shutting down: not the subscription's fault
	}
	var err error
	switch res {
	case outcomeOK:
		err = s.db.Do(ctx, func(tx *store.Tx) error {
			_, err := tx.Exec(`UPDATE push_subscriptions SET failure_count = 0, last_ok_at = ? WHERE id = ?`, tx.Now(), sub.ID)
			return err
		})
	case outcomeGone:
		err = s.remove(ctx, sub.ID)
	default:
		err = s.db.Do(ctx, func(tx *store.Tx) error {
			if _, err := tx.Exec(`UPDATE push_subscriptions SET failure_count = failure_count + 1 WHERE id = ?`, sub.ID); err != nil {
				return err
			}
			_, err := tx.Exec(`DELETE FROM push_subscriptions WHERE id = ? AND failure_count >= ?`, sub.ID, MaxFailures)
			return err
		})
		s.logger.Warn("push: delivery failed", "subscription", sub.ID, "status", status)
	}
	if err != nil && ctx.Err() == nil {
		s.logger.Warn("push: record delivery result", "subscription", sub.ID, "err", err)
	}
}

func (s *Service) remove(ctx context.Context, id string) error {
	return s.db.Do(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`DELETE FROM push_subscriptions WHERE id = ?`, id)
		return err
	})
}

// post makes the request and classifies the answer: 2xx is delivered, 404 and
// 410 mean the subscription is gone, everything else (including redirects and
// network errors) is a failure. status is 0 when there was no answer.
func (s *Service) post(ctx context.Context, sub Subscription, payload []byte, urgency webpush.Urgency) (outcome, int) {
	if !s.allowAny {
		if err := ValidateEndpoint(sub.Endpoint); err != nil {
			// A row that predates the allowlist, or was written to the
			// database directly, is never contacted.
			return outcomeGone, 0
		}
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256DH, Auth: sub.Auth},
	}, &webpush.Options{
		HTTPClient:      s.client,
		Subscriber:      subscriber,
		TTL:             TTLSeconds,
		Urgency:         urgency,
		VAPIDPublicKey:  s.vapid.PublicKey,
		VAPIDPrivateKey: s.vapid.PrivateKey,
	})
	if err != nil {
		// Transport errors can quote the URL; keep them out of the log.
		return outcomeFailed, 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseDrain))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return outcomeOK, resp.StatusCode
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return outcomeGone, resp.StatusCode
	}
	return outcomeFailed, resp.StatusCode
}

// String makes outcome readable in test failures.
func (o outcome) String() string {
	switch o {
	case outcomeOK:
		return "ok"
	case outcomeGone:
		return "gone"
	}
	return "failed"
}
