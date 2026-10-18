package bubblesclient

import (
	"context"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// PostMessage posts a message to the channel with the given id. The token
// needs messages:write.
func (c *Client) PostMessage(ctx context.Context, channel string, m protocol.NewMessage) (*protocol.Message, error) {
	var out protocol.Message
	if err := c.do(ctx, "POST", apiPrefix+"/channels/"+pathEscape(channel)+"/messages", m, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Self returns the app and token this client authenticates as. Keep
// Self().App.ID: a later RequestPairing can name it in ReplacesAppID so the
// admin reconnects this app instead of creating a new one.
func (c *Client) Self(ctx context.Context) (*protocol.AppSelf, error) {
	var out protocol.AppSelf
	if err := c.do(ctx, "GET", apiPrefix+"/apps/self", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeSelf revokes the token this client authenticates with, for a
// "Disconnect" button: the server closes the token's live connections and
// answers every later request with it as ErrUnauthorized. The app, its bot
// and its other tokens stay. Any scope may do this. Forget the stored token
// afterwards; there is no way to undo it.
func (c *Client) RevokeSelf(ctx context.Context) error {
	return c.do(ctx, "DELETE", apiPrefix+"/apps/self/token", nil, nil)
}
