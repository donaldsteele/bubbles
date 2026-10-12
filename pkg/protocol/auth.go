package protocol

// ServerStatus is the body of GET /status, which needs no sign-in: what a
// client shows before anyone is signed in.
type ServerStatus struct {
	InstanceName string `json:"instance_name"`
	// RegistrationOpen reports whether anyone may sign up right now.
	RegistrationOpen bool `json:"registration_open"`
	// RegistrationClosesAt is when open sign-up ends; null when it has no
	// deadline or is closed.
	RegistrationClosesAt *string `json:"registration_closes_at"`
}

// LoginRequest is the body of POST /auth/login.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// AuthResponse answers every request that starts a session (login,
// invite accept, register). The session itself travels in the cookie.
type AuthResponse struct {
	User Me `json:"user"`
}

// InviteInfo is the body of GET /invites/{code}. Role is empty and Valid
// false when the code is unknown, expired, revoked or used up.
type InviteInfo struct {
	Valid        bool   `json:"valid"`
	Role         string `json:"role,omitempty"`
	InstanceName string `json:"instance_name"`
}

// AcceptInviteRequest is the body of POST /invites/{code}/accept.
type AcceptInviteRequest struct {
	// Name is the person's display name; the @handle is derived from it.
	Name        string `json:"name,omitempty"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

// RegisterRequest is the body of POST /register.
type RegisterRequest struct {
	// Name is the person's display name; the @handle is derived from it.
	Name     string `json:"name"`
	Password string `json:"password"`
}

// ResetPasswordRequest is the body of POST /password-resets/{code}.
type ResetPasswordRequest struct {
	Password string `json:"password"`
}

// ChangePasswordRequest is the body of POST /me/password.
type ChangePasswordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// Session describes one signed-in browser or device of the caller.
type Session struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Current     bool   `json:"current"`
	CreatedAt   string `json:"created_at"`
	LastSeenAt  string `json:"last_seen_at"`
	IdleExpires string `json:"idle_expires_at"`
	AbsExpires  string `json:"abs_expires_at"`
	UserAgent   string `json:"user_agent"`
	IP          string `json:"ip"`
}

// SessionList is the body of GET /me/sessions.
type SessionList struct {
	Items []Session `json:"items"`
}

// ScopeNotify lets a tray device show notifications for the user.
const ScopeNotify = "notify"

// DeviceScopes are the only scopes a device token carries.
var DeviceScopes = []string{ScopeEventsRead, ScopeMessagesRead, ScopeNotify}

// DeviceLoginRequest is the body of POST /auth/device-login.
type DeviceLoginRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	DeviceName string `json:"device_name"`
}

// DeviceRegisterRequest is the body of POST /auth/device-register: sign up
// and register the tray device in one step.
type DeviceRegisterRequest struct {
	Name       string `json:"name"`
	Password   string `json:"password"`
	DeviceName string `json:"device_name"`
}

// DeviceLoginResponse carries the new device token (shown once) and the
// one-time handoff code that opens the web app without a second sign-in.
type DeviceLoginResponse struct {
	DeviceToken string `json:"device_token"`
	DeviceID    string `json:"device_id"`
	HandoffCode string `json:"handoff_code"`
}

// HandoffCodeResponse is the body of POST /auth/handoff-code.
type HandoffCodeResponse struct {
	HandoffCode string `json:"handoff_code"`
}

// Device is the public view of a device token.
type Device struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes"`
	Current    bool     `json:"current"`
	CreatedAt  string   `json:"created_at"`
	LastUsedAt *string  `json:"last_used_at"`
}

// DeviceList is the body of GET /me/devices.
type DeviceList struct {
	Items []Device `json:"items"`
}
