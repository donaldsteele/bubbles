package protocol

// WebhookTokenPrefix starts the secret path token of every incoming webhook.
const WebhookTokenPrefix = "bwh_"

// Webhook is an incoming webhook: a secret URL that posts into one channel as
// a pseudo-app (a bot account named after the webhook). The secret itself is
// shown once, when the webhook is created; afterwards nothing identifies the
// URL but this record.
type Webhook struct {
	ID        string  `json:"id"`
	ChannelID string  `json:"channel_id"`
	AppID     string  `json:"app_id"`
	Name      string  `json:"name"`
	CreatedBy *string `json:"created_by"`
	CreatedAt string  `json:"created_at"`
	RevokedAt *string `json:"revoked_at"`
}

// WebhookList is the body of GET /admin/webhooks.
type WebhookList struct {
	Items []Webhook `json:"items"`
}

// CreateWebhookRequest is the body of POST /admin/webhooks. Name becomes the
// name of the webhook's pseudo-app and of its bot account.
type CreateWebhookRequest struct {
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
}

// CreatedWebhook is the answer of POST /admin/webhooks: the webhook and the
// URL to post to. The URL carries the secret and is not shown again.
type CreatedWebhook struct {
	Webhook
	URL string `json:"url"`
}
