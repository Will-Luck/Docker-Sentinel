package registry

import (
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
)

// RegistryCredential holds login credentials for a container registry.
type RegistryCredential struct {
	ID       string `json:"id"`       // UUID
	Registry string `json:"registry"` // e.g. "docker.io", "ghcr.io"
	Username string `json:"username"`
	Secret   string `json:"secret"` // password or PAT
}

// CredentialStore persists registry credentials.
type CredentialStore interface {
	GetRegistryCredentials() ([]RegistryCredential, error)
	SetRegistryCredentials(creds []RegistryCredential) error
}

// MaskCredentialSecrets returns a copy with secrets masked (first 4 chars + "****").
func MaskCredentialSecrets(creds []RegistryCredential) []RegistryCredential {
	masked := make([]RegistryCredential, len(creds))
	for i, c := range creds {
		masked[i] = c
		if len(c.Secret) > 4 {
			masked[i].Secret = c.Secret[:4] + "****"
		} else if c.Secret != "" {
			masked[i].Secret = "****"
		}
	}
	return masked
}

// RestoreCredentialSecrets restores masked secrets from existing saved credentials.
// If incoming has a secret ending in "****", the saved secret for that ID is preserved.
func RestoreCredentialSecrets(incoming, saved []RegistryCredential) []RegistryCredential {
	savedMap := make(map[string]RegistryCredential, len(saved))
	for _, c := range saved {
		savedMap[c.ID] = c
	}
	for i, c := range incoming {
		if strings.HasSuffix(c.Secret, "****") {
			if old, ok := savedMap[c.ID]; ok {
				incoming[i].Secret = old.Secret
			}
		}
	}
	return incoming
}

// FindByRegistry returns the credential for a given registry host, or nil.
func FindByRegistry(creds []RegistryCredential, registry string) *RegistryCredential {
	for i, c := range creds {
		if c.Registry == registry {
			return &creds[i]
		}
	}
	return nil
}

// EncodedAuthFor returns the X-Registry-Auth value for the stored credential
// that matches imageRef's registry, or "" when there is none. Docker Hub
// credentials are sent with the address the daemon knows Hub by.
func EncodedAuthFor(creds []RegistryCredential, imageRef string) string {
	host := RegistryHost(imageRef)
	c := FindByRegistry(creds, host)
	if c == nil || c.Username == "" {
		return ""
	}
	server := host
	if host == "docker.io" {
		server = "https://index.docker.io/v1/"
	}
	return docker.EncodeRegistryAuth(c.Username, c.Secret, server)
}
