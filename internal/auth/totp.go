package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"time"

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

// maxTOTPAttempts is how many wrong codes a pending 2FA login token takes
// before it is thrown away and the password must be entered again.
const maxTOTPAttempts = 5

// totpStepSeconds is the length of a TOTP time step.
const totpStepSeconds = 30

// ValidateTOTPStep checks a code against a secret around now (one step either
// side, as ValidateTOTPCode does) and returns the time step it matched.
func ValidateTOTPStep(secret, code string, now time.Time) (int64, bool) {
	for _, d := range []int64{0, -1, 1} {
		step := now.Unix()/totpStepSeconds + d
		want, err := totp.GenerateCode(secret, time.Unix(step*totpStepSeconds, 0))
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// hashRecoveryCode returns the hex-encoded SHA-256 hash of a recovery code.
func hashRecoveryCode(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// GenerateRecoveryCodes creates a set of one-time recovery codes.
// Returns the plain-text codes (show to user once) and their stored
// representations. Stored codes are SHA-256 hashed so they cannot
// be recovered from the database.
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
		stored[i] = hashRecoveryCode(code)
	}
	return plain, stored, nil
}

// ValidateRecoveryCode checks a recovery code against the stored hashes.
// Returns the index of the matched code, or -1 if no match.
// Hashes the input with SHA-256 and uses constant-time comparison to
// avoid timing attacks.
func ValidateRecoveryCode(input string, stored []string) int {
	inputHash := hashRecoveryCode(input)
	for i, storedHash := range stored {
		if subtle.ConstantTimeCompare([]byte(inputHash), []byte(storedHash)) == 1 {
			return i
		}
	}
	return -1
}
