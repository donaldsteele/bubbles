package bubblesclient

import (
	"context"
	"net/url"
	"strconv"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// ListChannels returns the channels the token's bot belongs to (inside the
// token's channel allowlist). The token needs channels:read or messages:read.
func (c *Client) ListChannels(ctx context.Context) ([]protocol.Channel, error) {
	var out protocol.ChannelList
	if err := c.do(ctx, "GET", apiPrefix+"/channels", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// ListMessages returns up to limit messages of a channel, oldest first. When
// before is a message id, only older messages are returned. limit 0 means the
// server default. The token needs messages:read.
func (c *Client) ListMessages(ctx context.Context, channel, before string, limit int) (*protocol.MessagePage, error) {
	q := url.Values{}
	if before != "" {
		q.Set("before", before)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out protocol.MessagePage
	if err := c.do(ctx, "GET", apiPrefix+"/channels/"+pathEscape(channel)+"/messages"+encodeQuery(q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Search runs a search query (see the search grammar) over the channels the
// bot belongs to. limit 0 means the server default. The token needs
// messages:read.
func (c *Client) Search(ctx context.Context, query, cursor string, limit int) (*protocol.SearchPage, error) {
	q := url.Values{"q": {query}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out protocol.SearchPage
	if err := c.do(ctx, "GET", apiPrefix+"/search"+encodeQuery(q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListAlerts returns alert messages, newest first. state (open, acked or
// resolved) and channelID are optional filters. The token needs
// messages:read.
func (c *Client) ListAlerts(ctx context.Context, state, channelID, cursor string, limit int) (*protocol.AlertPage, error) {
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	if channelID != "" {
		q.Set("channel_id", channelID)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out protocol.AlertPage
	if err := c.do(ctx, "GET", apiPrefix+"/alerts"+encodeQuery(q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PostAlertResult is PostAlert that also reports the transition (an
// AlertTransition* constant) the post caused.
func (c *Client) PostAlertResult(ctx context.Context, a protocol.AlertInput) (*protocol.PostedAlert, error) {
	var out protocol.PostedAlert
	if err := c.do(ctx, "POST", apiPrefix+"/alerts", a, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AckAlert acknowledges an alert by message id. The server refuses app
// tokens (403): acknowledging is for people.
func (c *Client) AckAlert(ctx context.Context, id string) (*protocol.Message, error) {
	var out protocol.Message
	if err := c.do(ctx, "POST", apiPrefix+"/alerts/"+pathEscape(id)+"/ack", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResolveAlertByID resolves an alert by message id. The server refuses app
// tokens (403); use ResolveAlertKey for the app's own alerts.
func (c *Client) ResolveAlertByID(ctx context.Context, id, text string) (*protocol.Message, error) {
	var out protocol.Message
	in := protocol.ResolveAlertRequest{Text: text}
	if err := c.do(ctx, "POST", apiPrefix+"/alerts/"+pathEscape(id)+"/resolve", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResolveAlertKey resolves the open alert with the dedupe key and returns
// its message.
func (c *Client) ResolveAlertKey(ctx context.Context, dedupeKey, text string) (*protocol.Message, error) {
	var out protocol.Message
	in := protocol.ResolveAlertByKeyRequest{DedupeKey: dedupeKey, Text: text}
	if err := c.do(ctx, "POST", apiPrefix+"/alerts/resolve", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func encodeQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}
