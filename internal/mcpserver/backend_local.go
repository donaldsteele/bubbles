package mcpserver

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/donaldsteele/bubbles/internal/alerts"
	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/search"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Services are the in-process services the local backend calls.
type Services struct {
	Chat   *chat.Service
	Search *search.Service
	Alerts *alerts.Service
}

// Limits are the ingest limiters of the REST API, shared with the local
// backend so MCP posts draw from the same buckets as REST posts: Posts is keyed
// by the acting user id, Alerts by the app's bot user id. A nil limiter does
// not limit.
type Limits struct {
	Posts  *auth.BucketLimiter
	Alerts *auth.BucketLimiter
}

// RateLimitedError is returned when a post is refused by a limiter. Its text
// is what the tool result shows.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e RateLimitedError) Error() string {
	secs := max(int(math.Ceil(e.RetryAfter.Seconds())), 1)
	return fmt.Sprintf("rate limited, retry after %ds", secs)
}

type local struct {
	svc   Services
	actor chat.Actor
	aud   audit.Actor
	lim   Limits
}

// NewLocal returns the backend that calls services directly as actor, the
// app's bot with its token's scopes and channel allowlist. aud is recorded in
// the audit log for alert changes, and lim is the REST API's ingest limits.
func NewLocal(svc Services, actor chat.Actor, aud audit.Actor, lim Limits) Backend {
	return &local{svc: svc, actor: actor, aud: aud, lim: lim}
}

func (l *local) Scopes(context.Context) ([]string, error) { return l.actor.Scopes, nil }

func (l *local) ListChannels(ctx context.Context) ([]protocol.Channel, error) {
	return l.svc.Chat.ListChannels(ctx, l.actor, chat.ScopeJoined)
}

func (l *local) ReadMessages(ctx context.Context, channelID, before string, limit int) (protocol.MessagePage, error) {
	return l.svc.Chat.ListMessages(ctx, l.actor, channelID, chat.PageOptions{Before: before, Limit: limit})
}

func (l *local) Search(ctx context.Context, query, cursor string) (protocol.SearchPage, error) {
	return l.svc.Search.Search(ctx, l.actor, search.Params{Q: query, Cursor: cursor})
}

func (l *local) PostMessage(ctx context.Context, channelID string, m protocol.NewMessage) (protocol.Message, error) {
	if err := l.allow(l.lim.Posts, l.actor.ID); err != nil {
		return protocol.Message{}, err
	}
	p := chat.CreateMessageParams{ChannelID: channelID, Body: m.Body, Nonce: m.Nonce}
	if m.ThreadRootID != nil {
		p.ThreadRootID = *m.ThreadRootID
	}
	msg, _, err := l.svc.Chat.CreateMessage(ctx, l.actor, p)
	return msg, err
}

func (l *local) ListAlerts(ctx context.Context, state, channelID, cursor string) (protocol.AlertPage, error) {
	return l.svc.Alerts.List(ctx, l.actor, alerts.ListParams{State: state, ChannelID: channelID, Cursor: cursor})
}

// allow takes a token from lim for key, or returns a RateLimitedError.
func (l *local) allow(lim *auth.BucketLimiter, key string) error {
	if lim == nil {
		return nil
	}
	if ok, wait := lim.Allow(key); !ok {
		return RateLimitedError{RetryAfter: wait}
	}
	return nil
}

func (l *local) PostAlert(ctx context.Context, in protocol.AlertInput) (protocol.PostedAlert, error) {
	if err := l.allow(l.lim.Alerts, l.actor.ID); err != nil {
		return protocol.PostedAlert{}, err
	}
	res, err := l.svc.Alerts.Post(ctx, l.actor, in)
	if err != nil {
		return protocol.PostedAlert{}, err
	}
	return protocol.PostedAlert{Message: res.Message, Transition: res.Transition}, nil
}

func (l *local) AckAlert(ctx context.Context, id string) (protocol.Message, error) {
	return l.svc.Alerts.Ack(ctx, l.actor, l.aud, id)
}

func (l *local) ResolveAlert(ctx context.Context, id, dedupeKey, text string) (protocol.Message, error) {
	if id != "" {
		return l.svc.Alerts.Resolve(ctx, l.actor, l.aud, id, text)
	}
	return l.svc.Alerts.ResolveByKey(ctx, l.actor, l.aud, dedupeKey, text)
}
