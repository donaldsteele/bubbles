package bubblesclient

import (
	"context"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// RegisterCommands replaces the app's slash commands. Only Name,
// Description and Usage are sent. The token needs the commands scope.
func (c *Client) RegisterCommands(ctx context.Context, cmds []protocol.Command) error {
	in := make([]protocol.CommandInput, 0, len(cmds))
	for _, cmd := range cmds {
		in = append(in, protocol.CommandInput{Name: cmd.Name, Description: cmd.Description, Usage: cmd.Usage})
	}
	return c.do(ctx, "PUT", apiPrefix+"/apps/self/commands", in, nil)
}

// Respond answers a command invocation received as a command.invoked event.
func (c *Client) Respond(ctx context.Context, invocationID string, r protocol.CommandResponse) error {
	return c.do(ctx, "POST", apiPrefix+"/commands/invocations/"+pathEscape(invocationID)+"/respond", r, nil)
}
