package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const (
	totpIssuer        = "Docker-Sentinel"
	recoveryCodeCount = 8
	recoveryCodeLen   = 8 // hex characters (4 bytes)
)

// GenerateTOTPSecret creates a new TOTP secret for the given user.
// Returns the key (contains secret + provisioning URL for QR).
func GenerateTOTPSecret(username string) (*otp.Key, error) {
	return totp.Generate(totp.GenerateOpts{
		Issuer:      totpIssuer,
		AccountName: username,
	})
}

// ValidateTOTPCode checks a 6-digit TOTP code against a secret.
func ValidateTOTPCode(secret, code string) bool {
	return totp.Validate(code, secret)
}

// GenerateRecoveryCodes creates a set of one-time recovery codes.
// Returns the plain-text codes (show to user once) and their stored
// representations. Currently stored as plain hex; a future version
// could bcrypt them.
func GenerateRecoveryCodes() (plain []string, stored []string, err error) {
	plain = make([]string, recoveryCodeCount)
	stored = make([]string, recoveryCodeCount)
	for i := 0; i < recoveryCodeCount; i++ {
		b := make([]byte, recoveryCodeLen/2)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, fmt.Errorf("generate recovery code: %w", err)
		}
		code := hex.EncodeToString(b)
		plain[i] = code
		stored[i] = code
	}
	return plain, stored, nil
}

// ValidateRecoveryCode checks a recovery code against the stored codes.
// Returns the index of the matched code, or -1 if no match.
// Uses constant-time comparison to avoid timing attacks.
func ValidateRecoveryCode(input string, stored []string) int {
	for i, code := range stored {
		if subtle.ConstantTimeCompare([]byte(input), []byte(code)) == 1 {
			return i
		}
	}
	return -1
}
