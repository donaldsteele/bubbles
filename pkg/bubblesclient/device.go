package bubblesclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// DeviceLogin signs a desktop device in with a username and password
// (POST /auth/device-login). The client needs no token. The result carries
// the new device token (shown once) and a one-time handoff code for
// HandoffURL. A wrong password is an *APIError with status 401, which also
// matches ErrUnauthorized: tell it apart from a revoked token by the call
// site, not by the error value.
func (c *Client) DeviceLogin(ctx context.Context, username, password, deviceName string) (*protocol.DeviceLoginResponse, error) {
	var out protocol.DeviceLoginResponse
	err := c.do(ctx, http.MethodPost, apiPrefix+"/auth/device-login",
		protocol.DeviceLoginRequest{Username: username, Password: password, DeviceName: deviceName}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeviceRegister signs a new person up and registers their desktop device in
// one step (POST /auth/device-register). The client needs no token. It fails
// with an *APIError: 403 while sign-up is closed, 409 when the name is taken,
// 429 when this address has tried too often. The result is like DeviceLogin's.
func (c *Client) DeviceRegister(ctx context.Context, name, password, deviceName string) (*protocol.DeviceLoginResponse, error) {
	var out protocol.DeviceLoginResponse
	err := c.do(ctx, http.MethodPost, apiPrefix+"/auth/device-register",
		protocol.DeviceRegisterRequest{Name: name, Password: password, DeviceName: deviceName}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Status fetches the public server status (GET /status): the instance name
// and whether sign-up is open. It works without a token.
func (c *Client) Status(ctx context.Context) (*protocol.ServerStatus, error) {
	var out protocol.ServerStatus
	if err := c.do(ctx, http.MethodGet, apiPrefix+"/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// HandoffCode asks for a new one-time handoff code (POST
// /auth/handoff-code); the client must carry a device token.
func (c *Client) HandoffCode(ctx context.Context) (string, error) {
	var out protocol.HandoffCodeResponse
	if err := c.do(ctx, http.MethodPost, apiPrefix+"/auth/handoff-code", nil, &out); err != nil {
		return "", err
	}
	if out.HandoffCode == "" {
		return "", errors.New("bubblesclient: the server returned no handoff code")
	}
	return out.HandoffCode, nil
}

// HandoffURL is the address that turns a handoff code into a web session.
func (c *Client) HandoffURL(code string) string { return c.HandoffURLNext(code, "") }

// HandoffURLNext is HandoffURL with the same-origin path the server redirects
// to after signing in, for example "/c/<channel id>". The server ignores a
// next that is not a plain absolute path; an empty next is left out.
func (c *Client) HandoffURLNext(code, next string) string {
	u := c.base.String() + "/auth/handoff?code=" + url.QueryEscape(code)
	if next != "" {
		u += "&next=" + url.QueryEscape(next)
	}
	return u
}

// RevokeDevice revokes a device token (DELETE /me/devices/{id}). A device
// token can revoke only itself.
func (c *Client) RevokeDevice(ctx context.Context, deviceID string) error {
	return c.do(ctx, http.MethodDelete, apiPrefix+"/me/devices/"+pathEscape(deviceID), nil, nil)
}

// Bootstrap fetches the client snapshot (GET /bootstrap).
func (c *Client) Bootstrap(ctx context.Context) (*protocol.Bootstrap, error) {
	var out protocol.Bootstrap
	if err := c.do(ctx, http.MethodGet, apiPrefix+"/bootstrap", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
