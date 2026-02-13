package auth

import (
	"fmt"
	"time"
)

// WebAuthnCredential represents a stored WebAuthn passkey credential.
type WebAuthnCredential struct {
	ID              []byte                `json:"id"`         // credential ID (raw bytes)
	PublicKey       []byte                `json:"public_key"` // COSE-encoded public key
	AttestationType string                `json:"attestation_type"`
	Transport       []string              `json:"transport,omitempty"` // e.g. "usb", "ble", "internal"
	Flags           WebAuthnFlags         `json:"flags"`
	Authenticator   WebAuthnAuthenticator `json:"authenticator"`
	UserID          string                `json:"user_id"` // links to User.ID
	Name            string                `json:"name"`    // user-friendly label (e.g. "MacBook Touch ID")
	CreatedAt       time.Time             `json:"created_at"`
}

// WebAuthnFlags mirrors the credential flags from go-webauthn.
type WebAuthnFlags struct {
	UserPresent    bool `json:"user_present"`
	UserVerified   bool `json:"user_verified"`
	BackupEligible bool `json:"backup_eligible"`
	BackupState    bool `json:"backup_state"`
}

// WebAuthnAuthenticator holds authenticator metadata.
type WebAuthnAuthenticator struct {
	AAGUID       []byte `json:"aaguid"`
	SignCount    uint32 `json:"sign_count"`
	CloneWarning bool   `json:"clone_warning"`
	Attachment   string `json:"attachment"` // "platform" or "cross-platform"
}

// WebAuthnCredentialStore is the interface for WebAuthn credential persistence.
type WebAuthnCredentialStore interface {
	CreateWebAuthnCredential(cred WebAuthnCredential) error
	GetWebAuthnCredential(credID []byte) (*WebAuthnCredential, error)
	ListWebAuthnCredentialsForUser(userID string) ([]WebAuthnCredential, error)
	DeleteWebAuthnCredential(credID []byte) error
	GetUserByWebAuthnHandle(handle []byte) (*User, error)
	AnyWebAuthnCredentialsExist() (bool, error)
}

// Sentinel errors for WebAuthn.
var (
	ErrWebAuthnNotConfigured = fmt.Errorf("webauthn not configured")
	ErrCeremonyNotFound      = fmt.Errorf("ceremony not found or expired")
	ErrCredentialNotFound    = fmt.Errorf("credential not found")
	ErrNoPasskeys            = fmt.Errorf("no passkeys registered")
)
