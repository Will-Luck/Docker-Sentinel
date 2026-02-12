package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

const (
	TokenPrefix   = "stk_"
	tokenRawBytes = 32 // 32 bytes of randomness
	tokenIDBytes  = 8  // 8 bytes = 16 hex chars
)

// GenerateAPIToken creates a new API token with the stk_ prefix.
// Returns the full plaintext token (shown once) and the SHA-256 hash for storage.
func GenerateAPIToken() (plaintext string, hash string, err error) {
	raw := make([]byte, tokenRawBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	plaintext = TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	hash = HashToken(plaintext)
	return plaintext, hash, nil
}

// GenerateTokenID creates a random 16-char hex ID for API token records.
func GenerateTokenID() (string, error) {
	b := make([]byte, tokenIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HashToken returns the SHA-256 hex digest of a token string.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// ExtractBearerToken extracts a bearer token from the Authorization header.
// Returns empty string if not present or malformed.
func ExtractBearerToken(authHeader string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return ""
	}
	return strings.TrimSpace(authHeader[len(prefix):])
}
