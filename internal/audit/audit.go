// Package audit appends rows to the audit_log table. Write runs inside the
// transaction of the change it records, so a change and its audit trail
// commit or roll back together.
package audit

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/donaldsteele/bubbles/internal/store"
)

// Actor kinds.
const (
	KindUser    = "user"
	KindConsole = "console"
	KindSystem  = "system"
	KindApp     = "app"
)

// Actions recorded by the auth and settings code. Detail never carries
// passwords, tokens, invite or reset codes.
const (
	ActionSetup             = "setup.complete"
	ActionLoginSuccess      = "auth.login"
	ActionLoginFailure      = "auth.login_failed"
	ActionLogout            = "auth.logout"
	ActionPasswordChange    = "auth.password_change"
	ActionPasswordReset     = "auth.password_reset"
	ActionResetCreate       = "auth.reset_create"
	ActionInviteCreate      = "invite.create"
	ActionInviteAccept      = "invite.accept"
	ActionInviteRevoke      = "invite.revoke"
	ActionRegister          = "user.register"
	ActionSessionRevoke     = "session.revoke"
	ActionDeviceLogin       = "device.login"
	ActionDeviceRevoke      = "device.revoke"
	ActionHandoff           = "auth.handoff"
	ActionSettingsUpdated   = "settings.update"
	ActionUserCreate        = "user.create"
	ActionRegistrationOpen  = "registration.open"
	ActionRegistrationClose = "registration.close"
)

// Actor identifies who performs an action.
type Actor struct {
	// ID is the user id; empty for the console and the system.
	ID string
	// Kind is one of the Kind* constants.
	Kind string
	// IP is the client address; empty when there is none.
	IP string
}

// System is the actor of background work.
var System = Actor{Kind: KindSystem}

// Console is the actor of the admin CLI (console access is the root of trust).
var Console = Actor{Kind: KindConsole}

// User returns the actor for an authenticated user at ip.
func User(id, ip string) Actor { return Actor{ID: id, Kind: KindUser, IP: ip} }

// Entry is one audit_log row.
type Entry struct {
	ActorID    string
	ActorKind  string
	ActorIP    string
	Action     string
	TargetType string
	TargetID   string
	// Detail is marshaled to JSON; nil stores {}.
	Detail any
}

// Entry builds an Entry performed by a.
func (a Actor) Entry(action, targetType, targetID string, detail any) Entry {
	return Entry{
		ActorID: a.ID, ActorKind: a.Kind, ActorIP: a.IP,
		Action: action, TargetType: targetType, TargetID: targetID, Detail: detail,
	}
}

// Write inserts e into audit_log inside tx and returns the new row id.
func Write(tx *store.Tx, e Entry) (string, error) {
	if e.Action == "" {
		return "", errors.New("audit: empty action")
	}
	if e.ActorKind == "" {
		return "", errors.New("audit: empty actor kind")
	}
	detail := "{}"
	if e.Detail != nil {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return "", fmt.Errorf("audit: encode detail: %w", err)
		}
		detail = string(b)
	}
	var actorID any
	if e.ActorID != "" {
		actorID = e.ActorID
	}
	id := tx.NewID()
	_, err := tx.Exec(`INSERT INTO audit_log (id, at, actor_id, actor_kind, actor_ip, action, target_type, target_id, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, tx.Now(), actorID, e.ActorKind, e.ActorIP, e.Action, e.TargetType, e.TargetID, detail)
	if err != nil {
		return "", fmt.Errorf("audit: insert: %w", err)
	}
	return id, nil
}
