package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// ViaDevice marks a principal that proved itself with a tray device token.
const ViaDevice = "device"

const (
	// DevicePrefixLen is the length of the token prefix shown to identify a
	// device ("bub_" plus four secret characters).
	DevicePrefixLen = 8
	// MaxDevices caps the active device tokens of one user.
	MaxDevices = 25
	// DeviceLastUsedInterval throttles last_used_at writes.
	DeviceLastUsedInterval = time.Minute

	maxDeviceNameLen = 64
	maxTokenLen      = 128
)

// DeviceGrant is the authority of the device token behind a ViaDevice
// principal. It is deliberately not a TokenGrant: a device acts as its user
// (a human), limited by the route allowlist, not by an app's channel list.
type DeviceGrant struct {
	ID     string
	Name   string
	Scopes []string
}

// Device is a registered tray device.
type Device struct {
	ID         string
	Name       string
	Prefix     string
	Scopes     []string
	CreatedAt  int64
	LastUsedAt int64 // unix ms; 0 = never
}

// DeviceLoginInput is the input of DeviceLogin.
type DeviceLoginInput struct {
	Username   string
	Password   string
	DeviceName string
	UserAgent  string
	IP         string
}

// DeviceLoginResult is what DeviceLogin hands back; Token is shown once.
type DeviceLoginResult struct {
	Token       string
	DeviceID    string
	HandoffCode string
	User        *User
}

type deviceState struct {
	mu      sync.Mutex
	touched map[string]int64
}

func cleanDeviceName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxDeviceNameLen {
		return "", false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return name, true
}

// DeviceLogin verifies the credentials exactly like Login (same rate
// limiter reservation, lockout and audit of failures) and registers a tray
// device: it returns a new device token and a one-time handoff code.
func (s *Service) DeviceLogin(ctx context.Context, in DeviceLoginInput) (*DeviceLoginResult, error) {
	name, nameOK := cleanDeviceName(in.DeviceName)
	user, pwHash, newHash, err := s.verifyCredentials(ctx, LoginInput{
		Username: in.Username, Password: in.Password, UserAgent: in.UserAgent, IP: in.IP,
	})
	if err != nil {
		return nil, err
	}
	if !nameOK {
		return nil, &ValidationError{Fields: map[string]string{"device_name": "must be 1 to 64 printable characters"}}
	}
	raw, err := NewToken()
	if err != nil {
		return nil, err
	}
	secret := protocol.AppTokenPrefix + raw
	var res DeviceLoginResult
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		var active int
		if err := tx.QueryRow(`SELECT 1 FROM users WHERE id = ? AND deactivated_at IS NULL`, user.ID).Scan(&active); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrInvalidCredentials
			}
			return err
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM device_tokens WHERE user_id = ? AND revoked_at IS NULL`, user.ID).Scan(&n); err != nil {
			return fmt.Errorf("count devices: %w", err)
		}
		if n >= MaxDevices {
			return &ValidationError{Fields: map[string]string{"device_name": "too many devices; sign out of one first"}}
		}
		if newHash != "" {
			if _, err := tx.Exec(`UPDATE users SET pw_hash = ? WHERE id = ? AND pw_hash = ?`, newHash, user.ID, pwHash); err != nil {
				return fmt.Errorf("rehash password: %w", err)
			}
		}
		id, err := issueDeviceTokenTx(tx, user.ID, name, secret, in.IP)
		res = DeviceLoginResult{Token: secret, DeviceID: id, User: user}
		return err
	})
	if err != nil {
		return nil, err
	}
	res.HandoffCode, err = s.newHandoff(user.ID, res.DeviceID)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// issueDeviceTokenTx stores the device token secret for userID and audits the
// device login. It returns the device id.
func issueDeviceTokenTx(tx *store.Tx, userID, name, secret, ip string) (string, error) {
	scopes, _ := json.Marshal(protocol.DeviceScopes)
	id := tx.NewID()
	if _, err := tx.Exec(`INSERT INTO device_tokens (id, user_id, name, token_hash, prefix, scopes, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, userID, name, HashToken(secret), secret[:DevicePrefixLen], string(scopes), tx.Now()); err != nil {
		return "", fmt.Errorf("insert device token: %w", err)
	}
	_, err := audit.Write(tx, audit.User(userID, ip).Entry(audit.ActionDeviceLogin, "device", id, map[string]string{"prefix": secret[:DevicePrefixLen]}))
	return id, err
}

// DeviceRegisterInput is the input of DeviceRegister.
type DeviceRegisterInput struct {
	Name       string
	Password   string
	DeviceName string
	UserAgent  string
	IP         string
}

// DeviceRegister creates a member account like Register (same window, same
// per-IP limit, unique name) and registers a tray device for it in the same
// transaction: it returns the new device token and a one-time handoff code.
func (s *Service) DeviceRegister(ctx context.Context, in DeviceRegisterInput) (*DeviceLoginResult, error) {
	if err := s.signupAllowed(in.IP); err != nil {
		return nil, err
	}
	devName, nameOK := cleanDeviceName(in.DeviceName)
	f, verr := validateNewName(in.Name, in.Password)
	if !nameOK {
		if verr == nil {
			verr = &ValidationError{Fields: map[string]string{}}
		}
		verr.Fields["device_name"] = "must be 1 to 64 printable characters"
	}
	if verr != nil {
		return nil, verr
	}
	pwHash, err := s.hasher.Hash(ctx, f.password)
	if err != nil {
		return nil, err
	}
	raw, err := NewToken()
	if err != nil {
		return nil, err
	}
	secret := protocol.AppTokenPrefix + raw
	var res DeviceLoginResult
	err = s.db.Do(ctx, func(tx *store.Tx) error {
		user, err := insertUser(tx, f, RoleMember, pwHash)
		if err != nil {
			return err
		}
		if err := s.userCreated(ctx, tx, user.ID); err != nil {
			return err
		}
		if _, err := audit.Write(tx, audit.User(user.ID, in.IP).Entry(audit.ActionRegister, "user", user.ID,
			map[string]string{"username": user.Username})); err != nil {
			return err
		}
		id, err := issueDeviceTokenTx(tx, user.ID, devName, secret, in.IP)
		res = DeviceLoginResult{Token: secret, DeviceID: id, User: user}
		return err
	})
	if err != nil {
		return nil, err
	}
	res.HandoffCode, err = s.newHandoff(res.User.ID, res.DeviceID)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// ListDevices returns the active device tokens of userID, newest first.
func (s *Service) ListDevices(ctx context.Context, userID string) ([]Device, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT id, name, prefix, scopes, created_at, last_used_at FROM device_tokens
		WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var (
			d      Device
			scopes string
			used   sql.NullInt64
		)
		if err := rows.Scan(&d.ID, &d.Name, &d.Prefix, &scopes, &d.CreatedAt, &used); err != nil {
			return nil, fmt.Errorf("list devices: %w", err)
		}
		_ = json.Unmarshal([]byte(scopes), &d.Scopes)
		d.LastUsedAt = used.Int64
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	return out, nil
}

// RevokeDevice revokes one of userID's devices and closes its sockets.
// Another user's device is ErrNotFound; revoking twice is a no-op.
func (s *Service) RevokeDevice(ctx context.Context, actor audit.Actor, userID, deviceID string) error {
	return s.db.Do(ctx, func(tx *store.Tx) error {
		var revoked sql.NullInt64
		err := tx.QueryRow(`SELECT revoked_at FROM device_tokens WHERE id = ? AND user_id = ?`, deviceID, userID).Scan(&revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("find device: %w", err)
		}
		if revoked.Valid {
			return nil
		}
		if _, err := tx.Exec(`UPDATE device_tokens SET revoked_at = ? WHERE id = ?`, tx.Now(), deviceID); err != nil {
			return fmt.Errorf("revoke device: %w", err)
		}
		if err := appendDeviceClose(tx, userID, deviceID); err != nil {
			return err
		}
		_, err = audit.Write(tx, actor.Entry(audit.ActionDeviceRevoke, "device", deviceID, map[string]string{"user_id": userID}))
		return err
	})
}

func appendDeviceClose(tx *store.Tx, userID, deviceID string) error {
	payload, _ := json.Marshal(map[string]string{"token_id": deviceID})
	_, err := events.Append(tx, events.Event{Type: events.TypeSessionRevoked, UserID: userID, Payload: payload})
	return err
}

// RevokeDevicesTx revokes every active device token of userID and closes
// their sockets. Password changes and resets call it next to
// RevokeSessionsTx.
func RevokeDevicesTx(tx *store.Tx, userID string) error {
	rows, err := tx.Query(`SELECT id FROM device_tokens WHERE user_id = ? AND revoked_at IS NULL`, userID)
	if err != nil {
		return fmt.Errorf("list devices: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("list devices: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(`UPDATE device_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, tx.Now(), userID); err != nil {
		return fmt.Errorf("revoke devices: %w", err)
	}
	for _, id := range ids {
		if err := appendDeviceClose(tx, userID, id); err != nil {
			return err
		}
	}
	return nil
}

// authenticateDevice resolves a device token; (nil, nil) when it is not one.
func (s *Service) authenticateDevice(ctx context.Context, token string) (*Principal, error) {
	hash := HashToken(token)
	now := s.now().UnixMilli()
	var (
		id, name, scopes string
		stored           []byte
		used, revoked    sql.NullInt64
	)
	row := s.db.Read().QueryRowContext(ctx, `SELECT `+userColumns("u.")+`, d.id, d.name, d.token_hash, d.scopes, d.last_used_at, d.revoked_at
		FROM device_tokens d JOIN users u ON u.id = d.user_id WHERE d.token_hash = ?`, hash)
	user, err := scanUser(row, &id, &name, &stored, &scopes, &used, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load device token: %w", err)
	}
	if subtle.ConstantTimeCompare(stored, hash) != 1 || revoked.Valid || !user.Active() || user.Kind != KindHuman {
		return nil, nil
	}
	g := &DeviceGrant{ID: id, Name: name}
	_ = json.Unmarshal([]byte(scopes), &g.Scopes)
	s.touchDevice(ctx, id, used, now)
	return &Principal{User: user, Via: ViaDevice, Device: g}, nil
}

func (s *Service) touchDevice(ctx context.Context, id string, used sql.NullInt64, now int64) {
	interval := DeviceLastUsedInterval.Milliseconds()
	if used.Valid && now-used.Int64 < interval {
		return
	}
	s.devices.mu.Lock()
	if last, ok := s.devices.touched[id]; ok && now-last < interval {
		s.devices.mu.Unlock()
		return
	}
	if s.devices.touched == nil {
		s.devices.touched = map[string]int64{}
	}
	s.devices.touched[id] = now
	s.devices.mu.Unlock()
	err := s.db.Do(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE device_tokens SET last_used_at = ? WHERE id = ?`, now, id)
		return err
	})
	if err != nil {
		s.logger.Warn("update device last_used", "err", err)
		s.devices.mu.Lock()
		delete(s.devices.touched, id)
		s.devices.mu.Unlock()
	}
}

type deviceAuthenticator struct{ s *Service }

// DeviceAuthenticator returns the Authenticator for tray device tokens
// ("Authorization: Bearer bub_..."). Tokens it does not know fall through to
// the next authenticator (app tokens share the prefix).
func (s *Service) DeviceAuthenticator() Authenticator { return deviceAuthenticator{s} }

func (d deviceAuthenticator) Authenticate(r *http.Request) (*Principal, error) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return nil, nil
	}
	token := strings.TrimSpace(h[7:])
	if !strings.HasPrefix(token, protocol.AppTokenPrefix) || len(token) > maxTokenLen {
		return nil, nil
	}
	return d.s.authenticateDevice(r.Context(), token)
}
