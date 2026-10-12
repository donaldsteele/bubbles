package protocol

// Aliases under the names the Go SDK contract uses.
type (
	// NewMessage is the body of POST /api/v1/channels/{id}/messages.
	NewMessage = CreateMessageRequest
	// CommandResponse is the body of POST /api/v1/commands/invocations/{id}/respond.
	CommandResponse = RespondCommandRequest
	// PairingRequest is the body of POST /api/v1/pairing/requests.
	PairingRequest = PairingRequestInput
)
