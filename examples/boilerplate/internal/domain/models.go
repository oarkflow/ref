package domain

import (
	"time"
)

// UserStatus represents the account status of a user.
type UserStatus string

const (
	UserStatusActive    UserStatus = "active"
	UserStatusSuspended UserStatus = "suspended"
	UserStatusPending   UserStatus = "pending"
)

// Standard system role names.
const (
	RoleSuperAdmin = "super_admin"
	RoleAdmin      = "admin"
	RoleManager    = "manager"
	RoleUser       = "user"
	RoleGuest      = "guest"
)

// User represents an identity within the system.
type User struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	PasswordHash string     `json:"-"`
	Roles        []string   `json:"roles"`
	Status       UserStatus `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// HasRole checks if the user possesses a given role directly.
func (u *User) HasRole(role string) bool {
	for _, r := range u.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Session represents an active authenticated user session.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
}

// IsExpired checks whether the session has expired.
func (s *Session) IsExpired() bool {
	return time.Now().UTC().After(s.ExpiresAt)
}

// PasswordResetToken tracks single-use password reset tokens.
type PasswordResetToken struct {
	TokenHash string     `json:"token_hash"`
	UserID    string     `json:"user_id"`
	ExpiresAt time.Time  `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
}

// IsValid checks if the token can be consumed.
func (t *PasswordResetToken) IsValid() bool {
	if t.UsedAt != nil {
		return false
	}
	return time.Now().UTC().Before(t.ExpiresAt)
}

// RoleDefinition defines a role and its granted permissions & parent inheritance.
type RoleDefinition struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	Inherits    []string `json:"inherits"`
}

// Principal represents the authenticated security context carried through requests.
type Principal struct {
	ID          string         `json:"id"`
	Email       string         `json:"email"`
	Name        string         `json:"name"`
	Roles       []string       `json:"roles"`
	Permissions []string       `json:"permissions"`
	SessionID   string         `json:"session_id,omitempty"`
	Claims      map[string]any `json:"claims,omitempty"`
}

// ContextKey is a typed key for storing security objects in request context.
type ContextKey string

const (
	ContextKeyPrincipal ContextKey = "auth_principal"
	ContextKeyUser      ContextKey = "auth_user"
	ContextKeySession   ContextKey = "auth_session"
)
