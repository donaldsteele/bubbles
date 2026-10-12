package protocol

// Pairing request states.
const (
	PairingPending  = "pending"
	PairingApproved = "approved"
	PairingDenied   = "denied"
	PairingExpired  = "expired"
	PairingClaimed  = "claimed"
)

// PairingHeader carries the poll secret on GET /pairing/requests/{id}.
const PairingHeader = "X-Poll-Secret"

// PairingRequestInput is the body of POST /pairing/requests.
type PairingRequestInput struct {
	AppName         string   `json:"app_name"`
	Description     string   `json:"description,omitempty"`
	RequestedScopes []string `json:"requested_scopes,omitempty"`
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
// state is then claimed.
type PairingStatus struct {
	State     string `json:"state"`
	Token     string `json:"token,omitempty"`
	ExpiresAt string `json:"expires_at"`
}

// PendingPairing is a pairing request as the admin sees it: the code to
// compare with the one the app shows, where it came from, and the CA
// fingerprint the app displays too.
type PendingPairing struct {
	ID              string   `json:"id"`
	AppName         string   `json:"app_name"`
	Description     string   `json:"description"`
	RequestedScopes []string `json:"requested_scopes"`
	Code            string   `json:"code"`
	SourceIP        string   `json:"source_ip"`
	CAFingerprint   string   `json:"ca_fingerprint"`
	CreatedAt       string   `json:"created_at"`
	ExpiresAt       string   `json:"expires_at"`
}

// PendingPairingList is the body of GET /admin/pairing-requests.
type PendingPairingList struct {
	Items []PendingPairing `json:"items"`
}

// ApprovePairingRequest is the body of POST
// /admin/pairing-requests/{id}/approve: the scopes to grant, the channels to
// add the new bot to, and whether to create a public "<app>-alerts" channel
// for it. Empty Scopes grants the scopes the app requested.
type ApprovePairingRequest struct {
	Scopes        []string `json:"scopes,omitempty"`
	ChannelIDs    []string `json:"channel_ids,omitempty"`
	CreateChannel bool     `json:"create_channel,omitempty"`
}

// PairingApproval is the answer of an approval: the new app and the ids of
// the channels its bot was added to (the created one included).
type PairingApproval struct {
	App        App      `json:"app"`
	ChannelIDs []string `json:"channel_ids"`
}
