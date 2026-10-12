package apps

import (
	"database/sql"
	"errors"

	"github.com/donaldsteele/bubbles/internal/chat"
)

// The service reports problems with the chat package's typed errors, so the
// REST layer maps them with the one table it already has: not found -> 404,
// forbidden -> 403, conflict -> 409, validation -> 400 with details.fields.

func notFound() error { return &chat.Error{Kind: chat.ErrNotFound, Msg: "not found"} }

func conflict(msg string) error { return &chat.Error{Kind: chat.ErrConflict, Msg: msg} }

func invalid(field, reason string) error {
	return &chat.ValidationError{Fields: map[string]string{field: reason}}
}

func forbidden(msg string) error { return &chat.Error{Kind: chat.ErrForbidden, Msg: msg} }

// ErrAppOffline is returned by Invoke when the command's app has no live
// connection; the REST layer answers 503 unavailable.
var ErrAppOffline = errors.New("app is offline")

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
