package protocol

// PushKeys are the subscription keys a browser's PushSubscription.toJSON()
// returns: the client's P-256 public key and its 16-byte auth secret, both
// base64url without padding (RFC 8291).
type PushKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// PushSubscribeRequest is the body of POST /push/subscriptions. The endpoint
// must be an https URL of a known browser push service.
type PushSubscribeRequest struct {
	Endpoint string   `json:"endpoint"`
	Keys     PushKeys `json:"keys"`
}

// PushUnsubscribeRequest is the body of DELETE /push/subscriptions.
type PushUnsubscribeRequest struct {
	Endpoint string `json:"endpoint"`
}

// PushVapidKey is the response of GET /push/vapid-public-key: the server's
// VAPID public key (base64url, uncompressed P-256 point) for
// PushManager.subscribe({applicationServerKey}).
type PushVapidKey struct {
	Key string `json:"key"`
}

// PushPayload is the JSON the service worker receives in a push message.
// Body is present only when the admin set push_payload to "full" (a
// whitespace-collapsed excerpt of at most 120 characters).
type PushPayload struct {
	Title     string `json:"title"`
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
	Body      string `json:"body,omitempty"`
}
