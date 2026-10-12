package core

import (
	"errors"
	"sync"
	"time"
)

// PauseUntil returns the end of a notification pause started at now: an hour
// from now, or tomorrow at 08:00 local time.
func PauseUntil(kind string, now time.Time) (time.Time, error) {
	switch kind {
	case "1h":
		return now.Add(time.Hour), nil
	case "tomorrow":
		y, m, d := now.Date()
		return time.Date(y, m, d+1, 8, 0, 0, 0, now.Location()), nil
	}
	return time.Time{}, errors.New("unknown pause length")
}

// TokenStore keeps the device token outside the config file (the OS
// keychain in the app). The key is the server URL.
type TokenStore interface {
	Get(server string) (string, error)
	Set(server, token string) error
	Delete(server string) error
}

// ErrNoToken is returned by Get when nothing is stored.
var ErrNoToken = errors.New("no device token stored")

// MemoryTokens is an in-memory TokenStore for tests.
type MemoryTokens struct {
	mu sync.Mutex
	m  map[string]string
}

// Get implements TokenStore.
func (s *MemoryTokens) Get(server string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.m[server]; ok {
		return t, nil
	}
	return "", ErrNoToken
}

// Set implements TokenStore.
func (s *MemoryTokens) Set(server, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[server] = token
	return nil
}

// Delete implements TokenStore.
func (s *MemoryTokens) Delete(server string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, server)
	return nil
}
