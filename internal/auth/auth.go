package auth

import (
	"time"
)

// Permission represents a granular capability.
type Permission string

const (
	PermContainersView     Permission = "containers.view"
	PermContainersUpdate   Permission = "containers.update"
	PermContainersApprove  Permission = "containers.approve"
	PermContainersRollback Permission = "containers.rollback"
	PermContainersManage   Permission = "containers.manage"
	PermSettingsView       Permission = "settings.view"
	PermSettingsModify     Permission = "settings.modify"
	PermUsersManage        Permission = "users.manage"
	PermLogsView           Permission = "logs.view"
	PermHistoryView        Permission = "history.view"
)

// AllPermissions returns every defined permission.
func AllPermissions() []Permission {
	return []Permission{
		PermContainersView, PermContainersUpdate, PermContainersApprove,
		PermContainersRollback, PermContainersManage, PermSettingsView,
		PermSettingsModify, PermUsersManage, PermLogsView, PermHistoryView,
	}
}

// User represents an authenticated user.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	RoleID       string    `json:"role_id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Locked       bool      `json:"locked"`        // locked after too many failed logins
	LockedUntil  time.Time `json:"locked_until"`  // unlock time
	FailedLogins int       `json:"failed_logins"` // consecutive failures
}

// Session represents an active login session.
type Session struct {
	Token     string    `json:"token"`      // 64-char hex token (also the bucket key)
	UserID    string    `json:"user_id"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Role defines a named set of permissions.
type Role struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Permissions []Permission `json:"permissions"`
	BuiltIn     bool         `json:"built_in"`
}

// APIToken represents a bearer token for programmatic API access.
type APIToken struct {
	ID          string       `json:"id"`           // 16-char hex ID
	Name        string       `json:"name"`         // user-friendly label
	TokenHash   string       `json:"token_hash"`   // SHA-256 hex of the full token
	UserID      string       `json:"user_id"`
	Permissions []Permission `json:"permissions"`   // nil = inherit from user role
	CreatedAt   time.Time    `json:"created_at"`
	ExpiresAt   time.Time    `json:"expires_at"`    // zero = no expiry
	LastUsedAt  time.Time    `json:"last_used_at"`
}

// RequestContext is extracted from the request by middleware and placed in context.
type RequestContext struct {
	User        *User
	Session     *Session
	APIToken    *APIToken
	Permissions []Permission
	AuthEnabled bool
}

// HasPermission checks if the request context includes a specific permission.
func (rc *RequestContext) HasPermission(p Permission) bool {
	for _, perm := range rc.Permissions {
		if perm == p {
			return true
		}
	}
	return false
}

// contextKey is an unexported type for context keys.
type contextKey struct{}

// ContextKey is the key used to store RequestContext in context.Context.
var ContextKey = contextKey{}
