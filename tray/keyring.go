package main

import (
	"errors"

	"github.com/zalando/go-keyring"

	"github.com/donaldsteele/bubbles/tray/internal/core"
)

// keychainService is the service name entries are stored under in the OS
// keychain (Windows Credential Manager, macOS Keychain, Secret Service).
const keychainService = "bubbles-tray"

// keychain stores the device token in the OS keychain, keyed by server URL.
// The token never touches the config file or the logs.
type keychain struct{}

var _ core.TokenStore = keychain{}

func (keychain) Get(server string) (string, error) {
	v, err := keyring.Get(keychainService, server)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", core.ErrNoToken
	}
	return v, err
}

func (keychain) Set(server, token string) error {
	return keyring.Set(keychainService, server, token)
}

func (keychain) Delete(server string) error {
	err := keyring.Delete(keychainService, server)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
