// Package auth implements accounts and access control: argon2id passwords
// with a common-password blocklist, first-run setup, cookie sessions,
// invites, password-reset links, roles, login and API rate limiting, and the
// authentication middleware. It owns its SQL; HTTP handlers live in
// internal/httpapi.
package auth

import "fmt"

// Role is a user's permission level.
type Role string

// Roles, from most to least privileged.
const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleGuest  Role = "guest"
)

// ParseRole converts s to a Role.
func ParseRole(s string) (Role, error) {
	r := Role(s)
	if !r.Valid() {
		return "", fmt.Errorf("unknown role %q (want admin, member or guest)", s)
	}
	return r, nil
}

// Valid reports whether r is one of the three roles.
func (r Role) Valid() bool {
	switch r {
	case RoleAdmin, RoleMember, RoleGuest:
		return true
	}
	return false
}

func (r Role) rank() int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleMember:
		return 2
	case RoleGuest:
		return 1
	}
	return 0
}

// AtLeast reports whether r grants at least the rights of min.
func (r Role) AtLeast(min Role) bool { return r.Valid() && r.rank() >= min.rank() }

// IsAdmin reports whether r is the admin role.
func (r Role) IsAdmin() bool { return r == RoleAdmin }
