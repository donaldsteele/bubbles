// Package protocol holds the wire types of the bubbles REST API. It is the
// single source of truth shared by the server handlers and the Go SDK and
// depends on the standard library only. The REST contract is
// docs/openapi.yaml.
package protocol

import "fmt"

// Error codes of the REST error model (see docs/openapi.yaml).
const (
	CodeBadRequest          = "bad_request"
	CodeUnauthorized        = "unauthorized"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeConflict            = "conflict"
	CodeTooLarge            = "too_large"
	CodeUnsupportedMedia    = "unsupported_media"
	CodeRateLimited         = "rate_limited"
	CodeInsufficientStorage = "insufficient_storage"
	CodeUnavailable         = "unavailable"
	CodeInternal            = "internal"
)

// DetailsFields is the key of Error.Details that maps a request field name
// to the reason it was rejected (validation errors).
const DetailsFields = "fields"

// Error is the inner object of the error envelope.
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Error makes *Error usable as a Go error in SDK code.
func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// ErrorBody is the JSON error envelope: {"error":{"code","message","details"}}.
type ErrorBody struct {
	Error Error `json:"error"`
}

// FieldErrors returns details.fields of a validation error, or nil.
func (e *Error) FieldErrors() map[string]string {
	raw, _ := e.Details[DetailsFields].(map[string]any)
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}
