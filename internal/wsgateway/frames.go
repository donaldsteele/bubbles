package wsgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// maxIDLen bounds ids in query parameters and client frames (ULIDs are 26).
const maxIDLen = 64

// query is the parsed connection query string.
type query struct {
	resume    int64
	hasResume bool
	instance  string
	epoch     int64
	hasEpoch  bool
}

// parseQuery reads resume, instance and epoch. Each is optional; present
// values must be well-formed.
func parseQuery(v url.Values) (query, error) {
	var q query
	if s := v.Get(protocol.QueryResume); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 0 {
			return q, errors.New("resume must be a non-negative integer")
		}
		q.resume, q.hasResume = n, true
	}
	if s := v.Get(protocol.QueryEpoch); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 1 {
			return q, errors.New("epoch must be a positive integer")
		}
		q.epoch, q.hasEpoch = n, true
	}
	q.instance = v.Get(protocol.QueryInstance)
	if len(q.instance) > maxIDLen {
		return q, errors.New("instance is too long")
	}
	return q, nil
}

// parseClientFrame decodes and validates one client frame. Unknown types,
// unknown fields and malformed values are errors (the socket is then closed
// with 1008 policy violation).
func parseClientFrame(data []byte) (protocol.ClientFrame, error) {
	var f protocol.ClientFrame
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("invalid JSON frame: %w", err)
	}
	if dec.More() {
		return f, errors.New("trailing data after frame")
	}
	switch f.Type {
	case protocol.ClientTyping:
		if f.ChannelID == nil || !validID(*f.ChannelID) {
			return f, errors.New("typing needs a channel_id")
		}
	case protocol.ClientPresence:
		if f.State != protocol.PresenceActive && f.State != protocol.PresenceIdle {
			return f, errors.New("presence state must be active or idle")
		}
	case protocol.ClientFocus:
		if f.Visible == nil {
			return f, errors.New("focus needs visible")
		}
		if f.ChannelID != nil && !validID(*f.ChannelID) {
			return f, errors.New("focus channel_id is invalid")
		}
		if f.TS != "" {
			if _, err := time.Parse(time.RFC3339Nano, f.TS); err != nil {
				return f, errors.New("focus ts must be RFC 3339")
			}
		}
	case protocol.ClientPing:
	default:
		return f, errors.New("unknown frame type")
	}
	return f, nil
}

func validID(s string) bool { return s != "" && len(s) <= maxIDLen }
