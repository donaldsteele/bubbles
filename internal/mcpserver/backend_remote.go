package mcpserver

import (
	"context"
	"sync"
	"time"

	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// scopesTTL bounds how long the token's scopes are cached, so a scope change
// on the server reaches the stdio bridge within a minute.
const scopesTTL = 60 * time.Second

type remote struct {
	c   *bubblesclient.Client
	now func() time.Time

	mu      sync.Mutex
	scopes  []string
	fetched time.Time // zero until the first successful fetch
}

// NewRemote returns the backend that calls a Bubbles server's REST API with
// the client's app token (the stdio bridge).
func NewRemote(c *bubblesclient.Client) Backend { return &remote{c: c, now: time.Now} }

func (r *remote) Scopes(ctx context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.fetched.IsZero() && r.now().Sub(r.fetched) < scopesTTL {
		return r.scopes, nil
	}
	self, err := r.c.Self(ctx)
	if err != nil {
		return nil, err
	}
	r.scopes, r.fetched = self.Token.Scopes, r.now()
	return r.scopes, nil
}

func (r *remote) ListChannels(ctx context.Context) ([]protocol.Channel, error) {
	return r.c.ListChannels(ctx)
}

func (r *remote) ReadMessages(ctx context.Context, channelID, before string, limit int) (protocol.MessagePage, error) {
	p, err := r.c.ListMessages(ctx, channelID, before, limit)
	if err != nil {
		return protocol.MessagePage{}, err
	}
	return *p, nil
}

func (r *remote) Search(ctx context.Context, query, cursor string) (protocol.SearchPage, error) {
	p, err := r.c.Search(ctx, query, cursor, 0)
	if err != nil {
		return protocol.SearchPage{}, err
	}
	return *p, nil
}

func (r *remote) PostMessage(ctx context.Context, channelID string, m protocol.NewMessage) (protocol.Message, error) {
	msg, err := r.c.PostMessage(ctx, channelID, m)
	if err != nil {
		return protocol.Message{}, err
	}
	return *msg, nil
}

func (r *remote) ListAlerts(ctx context.Context, state, channelID, cursor string) (protocol.AlertPage, error) {
	p, err := r.c.ListAlerts(ctx, state, channelID, cursor, 0)
	if err != nil {
		return protocol.AlertPage{}, err
	}
	return *p, nil
}

func (r *remote) PostAlert(ctx context.Context, in protocol.AlertInput) (protocol.PostedAlert, error) {
	p, err := r.c.PostAlertResult(ctx, in)
	if err != nil {
		return protocol.PostedAlert{}, err
	}
	return *p, nil
}

func (r *remote) AckAlert(ctx context.Context, id string) (protocol.Message, error) {
	m, err := r.c.AckAlert(ctx, id)
	if err != nil {
		return protocol.Message{}, err
	}
	return *m, nil
}

func (r *remote) ResolveAlert(ctx context.Context, id, dedupeKey, text string) (protocol.Message, error) {
	var (
		m   *protocol.Message
		err error
	)
	if id != "" {
		m, err = r.c.ResolveAlertByID(ctx, id, text)
	} else {
		m, err = r.c.ResolveAlertKey(ctx, dedupeKey, text)
	}
	if err != nil {
		return protocol.Message{}, err
	}
	return *m, nil
}
