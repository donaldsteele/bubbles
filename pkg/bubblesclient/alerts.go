package bubblesclient

import (
	"context"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// PostAlert creates or updates the alert identified by a.DedupeKey and
// returns its message. The token needs alerts:write.
func (c *Client) PostAlert(ctx context.Context, a protocol.AlertInput) (*protocol.Message, error) {
	var out protocol.PostedAlert
	if err := c.do(ctx, "POST", apiPrefix+"/alerts", a, &out); err != nil {
		return nil, err
	}
	return &out.Message, nil
}

// ResolveAlert resolves the open alert with the given dedupe key; text is an
// optional resolution note.
func (c *Client) ResolveAlert(ctx context.Context, dedupeKey, text string) error {
	return c.do(ctx, "POST", apiPrefix+"/alerts/resolve",
		protocol.ResolveAlertByKeyRequest{DedupeKey: dedupeKey, Text: text}, nil)
}
