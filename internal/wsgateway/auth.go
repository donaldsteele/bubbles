package wsgateway

import (
	"slices"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// viewerFor builds the event viewer of an authenticated principal. A cookie
// session sees what its user may see. An app bearer token adds its channel
// allowlist (nil stays "no allowlist", an empty list admits nothing), and
// without the events:read scope the viewer receives only its own
// command.invoked events. The token id, app id and expiry let the hub close
// the connection when the token is revoked, its app disabled, or it expires.
func viewerFor(p *auth.Principal) events.Viewer {
	v := events.Viewer{
		UserID: p.User.ID,
		Role:   string(p.User.Role),
		Kind:   p.User.Kind,
	}
	if p.Device != nil {
		// A device sees what its user sees; its id lets the hub close the
		// connection when the device is revoked.
		v.TokenID = p.Device.ID
	}
	if p.Token != nil {
		v.TokenID = p.Token.ID
		v.AppID = p.Token.AppID
		v.TokenExpiresAt = p.Token.ExpiresAt
		if p.Token.ChannelIDs != nil {
			v.ChannelIDs = slices.Clone(p.Token.ChannelIDs)
			if v.ChannelIDs == nil {
				v.ChannelIDs = []string{}
			}
		}
		v.CommandsOnly = !slices.Contains(p.Token.Scopes, protocol.ScopeEventsRead)
		v.NoContent = !slices.Contains(p.Token.Scopes, protocol.ScopeMessagesRead)
	}
	return v
}
