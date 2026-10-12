// Package mcpserver exposes Bubbles to MCP clients (AI agents) as eight
// tools. The tools talk to a Backend bound to one identity, the bot user of
// an app token: LocalBackend calls the server's own services in-process
// (the Streamable HTTP endpoint /api/v1/mcp), RemoteBackend calls the REST
// API through pkg/bubblesclient (the `bubbles mcp --conn` stdio bridge).
// Both apply the same permission rules because both end in the same
// services: no permission logic lives here beyond the scope pre-check.
package mcpserver

import (
	"context"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Backend is the data access of the tools, bound to one app token.
type Backend interface {
	// Scopes returns the scopes of the token.
	Scopes(ctx context.Context) ([]string, error)
	// ListChannels returns the channels the bot can use.
	ListChannels(ctx context.Context) ([]protocol.Channel, error)
	// ReadMessages returns up to limit messages of a channel, oldest first,
	// older than the message id before when it is set.
	ReadMessages(ctx context.Context, channelID, before string, limit int) (protocol.MessagePage, error)
	// Search runs a search query over the bot's channels; cursor is the
	// next_cursor of the previous page (empty for the first).
	Search(ctx context.Context, query, cursor string) (protocol.SearchPage, error)
	// PostMessage posts as the bot.
	PostMessage(ctx context.Context, channelID string, m protocol.NewMessage) (protocol.Message, error)
	// ListAlerts returns alert messages, newest first; cursor as for Search.
	ListAlerts(ctx context.Context, state, channelID, cursor string) (protocol.AlertPage, error)
	// PostAlert creates or updates an alert.
	PostAlert(ctx context.Context, in protocol.AlertInput) (protocol.PostedAlert, error)
	// AckAlert acknowledges an alert by message id.
	AckAlert(ctx context.Context, id string) (protocol.Message, error)
	// ResolveAlert resolves an alert by message id (id) or by dedupe key.
	ResolveAlert(ctx context.Context, id, dedupeKey, text string) (protocol.Message, error)
}
