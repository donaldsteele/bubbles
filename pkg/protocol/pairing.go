package protocol

// Pairing request states.
const (
	PairingPending  = "pending"
	PairingApproved = "approved"
	PairingDenied   = "denied"
	PairingExpired  = "expired"
	PairingClaimed  = "claimed"
	// PairingCancelled is a request the app withdrew (DELETE
	// /pairing/requests/{id}) before it claimed a token.
	PairingCancelled = "cancelled"
)

// PairingHeader carries the poll secret on GET and DELETE
// /pairing/requests/{id}.
const PairingHeader = "X-Poll-Secret"

// PairingRequestInput is the body of POST /pairing/requests.
// ReplacesAppID is optional: the id of the app this client was paired as
// before (the app id GET /apps/self reports). The admin then sees which
// app the request would replace and can reconnect it instead of creating a
// new one. An id that names no app is accepted and ignored.
type PairingRequestInput struct {
	AppName         string   `json:"app_name"`
	Description     string   `json:"description,omitempty"`
	RequestedScopes []string `json:"requested_scopes,omitempty"`
	ReplacesAppID   string   `json:"replaces_app_id,omitempty"`
}

// PairingCreated is the answer of POST /pairing/requests. The app shows Code
// to the admin and polls with PollSecret, which is shown only here.
type PairingCreated struct {
	ID         string `json:"id"`
	PollSecret string `json:"poll_secret"`
	Code       string `json:"code"`
	ExpiresAt  string `json:"expires_at"`
}

// PairingStatus is the answer of GET /pairing/requests/{id}. Token is set
// exactly once, on the first poll after the admin approved the request; the
// state is then claimed. Channels is set in that same answer only: the
// public and private channels, not archived, that the app's bot is a member
// of at the claim, sorted by name then id. It is left out when the bot is in
// no channel, and in every other answer.
type PairingStatus struct {
	State     string           `json:"state"`
	Token     string           `json:"token,omitempty"`
	ExpiresAt string           `json:"expires_at"`
	Channels  []PairingChannel `json:"channels,omitempty"`
}

// PairingChannel is a channel the bot of a paired app is a member of.
type PairingChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PendingPairing is a pairing request as the admin sees it: the code to
// compare with the one the app shows, where it came from, and the CA
// fingerprint the app displays too. Replaces is set when the request names
// an existing app (PairingRequestInput.ReplacesAppID).
type PendingPairing struct {
	ID              string           `json:"id"`
	AppName         string           `json:"app_name"`
	Description     string           `json:"description"`
	RequestedScopes []string         `json:"requested_scopes"`
	Code            string           `json:"code"`
	SourceIP        string           `json:"source_ip"`
	CAFingerprint   string           `json:"ca_fingerprint"`
	CreatedAt       string           `json:"created_at"`
	ExpiresAt       string           `json:"expires_at"`
	Replaces        *PairingReplaces `json:"replaces,omitempty"`
}

// PairingReplaces is the existing app a pairing request names: its id, name,
// bot username, when it was disabled (nil while enabled) and the public and
// private channels, not archived, that its bot is in, sorted by name then id
// (never nil). Approving with ReplaceApp hands that bot and those channels
// to the new token.
type PairingReplaces struct {
	AppID      string           `json:"app_id"`
	Name       string           `json:"name"`
	Username   string           `json:"username"`
	DisabledAt *string          `json:"disabled_at"`
	Channels   []PairingChannel `json:"channels"`
}

// PendingPairingList is the body of GET /admin/pairing-requests.
type PendingPairingList struct {
	Items []PendingPairing `json:"items"`
}

// ApprovePairingRequest is the body of POST
// /admin/pairing-requests/{id}/approve: the scopes to grant, the channels to
// add the bot to, and whether to add it to a public "<username>-alerts"
// channel (created unless a public one with that name exists). Empty Scopes
// grants the scopes the app requested. ReplaceApp reconnects the existing
// app the request names (PendingPairing.Replaces) instead of creating a new
// one: it keeps its id, bot and channels, and its other tokens are revoked
// when the app claims the new one.
type ApprovePairingRequest struct {
	Scopes        []string `json:"scopes,omitempty"`
	ChannelIDs    []string `json:"channel_ids,omitempty"`
	CreateChannel bool     `json:"create_channel,omitempty"`
	ReplaceApp    bool     `json:"replace_app,omitempty"`
}

// PairingApproval is the answer of an approval: the app (new, or the
// replaced one) and the ids of the channels its bot was added to (the alerts
// channel first).
type PairingApproval struct {
	App        App      `json:"app"`
	ChannelIDs []string `json:"channel_ids"`
}
