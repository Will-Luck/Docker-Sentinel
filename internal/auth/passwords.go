package auth

import (
	"errors"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

const (
	bcryptCost     = 12
	bcryptMaxBytes = 72 // bcrypt silently truncates passwords longer than this
)

var (
	ErrPasswordTooShort = errors.New("password must be at least 8 characters")
	ErrPasswordTooLong  = errors.New("password exceeds maximum length of 72 bytes")
	ErrPasswordNoLetter = errors.New("password must contain at least one letter")
	ErrPasswordNoDigit  = errors.New("password must contain at least one digit")
)

// ValidatePassword checks the password meets the minimum policy.
func ValidatePassword(password string) error {
	if len([]byte(password)) > bcryptMaxBytes {
		return ErrPasswordTooLong
	}
	if len(password) < 8 {
		return ErrPasswordTooShort
	}
	var hasLetter, hasDigit bool
	for _, r := range password {
		if unicode.IsLetter(r) {
			hasLetter = true
		}
		if unicode.IsDigit(r) {
			hasDigit = true
		}
	}
	if !hasLetter {
		return ErrPasswordNoLetter
	}
	if !hasDigit {
		return ErrPasswordNoDigit
	}
	return nil
}

// HashPassword returns a bcrypt hash of the password.
// Returns an error if the password exceeds 72 bytes (bcrypt's silent truncation limit).
func HashPassword(password string) (string, error) {
	if len([]byte(password)) > bcryptMaxBytes {
		return "", ErrPasswordTooLong
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword verifies a password against a bcrypt hash.
// Returns false if the password exceeds 72 bytes to prevent truncation-based collisions.
func CheckPassword(hash, password string) bool {
	if len([]byte(password)) > bcryptMaxBytes {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
