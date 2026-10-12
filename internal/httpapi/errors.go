package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Error codes of the REST error model (see docs/openapi.yaml).
const (
	CodeBadRequest          = protocol.CodeBadRequest
	CodeUnauthorized        = protocol.CodeUnauthorized
	CodeForbidden           = protocol.CodeForbidden
	CodeNotFound            = protocol.CodeNotFound
	CodeConflict            = protocol.CodeConflict
	CodeTooLarge            = protocol.CodeTooLarge
	CodeUnsupportedMedia    = protocol.CodeUnsupportedMedia
	CodeRateLimited         = protocol.CodeRateLimited
	CodeInsufficientStorage = protocol.CodeInsufficientStorage
	CodeUnavailable         = protocol.CodeUnavailable
	CodeInternal            = protocol.CodeInternal
)

// ErrorBody is the JSON error envelope: {"error":{"code","message","details"}}.
type ErrorBody = protocol.ErrorBody

// ErrorDetail is the inner object of ErrorBody.
type ErrorDetail = protocol.Error

// WriteError writes the error model as JSON with the given status. Error
// responses are never cached.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}

// WriteErrorDetails is WriteError with a details object.
func WriteErrorDetails(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	writeJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message, Details: details}})
}

// WriteFieldErrors answers a rejected request with details.fields mapping
// each offending field to the reason (status 400 bad_request, or the given
// status/code for conflicts).
func WriteFieldErrors(w http.ResponseWriter, status int, code, message string, fields map[string]string) {
	f := make(map[string]any, len(fields))
	for k, v := range fields {
		f[k] = v
	}
	WriteErrorDetails(w, status, code, message, map[string]any{protocol.DetailsFields: f})
}

// WriteRateLimited answers 429 rate_limited with a Retry-After header.
func WriteRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(auth.RetryAfterSeconds(retryAfter)))
	WriteError(w, http.StatusTooManyRequests, CodeRateLimited, "too many requests; try again later")
}

// writeJSON encodes v with the given status and no-store caching.
func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
