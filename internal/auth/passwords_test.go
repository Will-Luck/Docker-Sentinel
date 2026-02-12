package auth

import (
	"testing"
)

func TestValidatePassword(t *testing.T) {
	t.Run("too short", func(t *testing.T) {
		err := ValidatePassword("Ab1")
		if err != ErrPasswordTooShort {
			t.Errorf("expected ErrPasswordTooShort, got %v", err)
		}
	})

	t.Run("exactly 7 chars is too short", func(t *testing.T) {
		err := ValidatePassword("Abcde1x")
		if err != ErrPasswordTooShort {
			t.Errorf("expected ErrPasswordTooShort, got %v", err)
		}
	})

	t.Run("no letter", func(t *testing.T) {
		err := ValidatePassword("12345678")
		if err != ErrPasswordNoLetter {
			t.Errorf("expected ErrPasswordNoLetter, got %v", err)
		}
	})

	t.Run("no digit", func(t *testing.T) {
		err := ValidatePassword("abcdefgh")
		if err != ErrPasswordNoDigit {
			t.Errorf("expected ErrPasswordNoDigit, got %v", err)
		}
	})

	t.Run("valid password", func(t *testing.T) {
		err := ValidatePassword("Secret99")
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("exactly 8 chars valid", func(t *testing.T) {
		err := ValidatePassword("Abcdefg1")
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("empty string", func(t *testing.T) {
		err := ValidatePassword("")
		if err != ErrPasswordTooShort {
			t.Errorf("expected ErrPasswordTooShort, got %v", err)
		}
	})
}

func TestHashAndCheckPassword(t *testing.T) {
	t.Run("roundtrip", func(t *testing.T) {
		password := "MySecret42"
		hash, err := HashPassword(password)
		if err != nil {
			t.Fatalf("HashPassword failed: %v", err)
		}
		if hash == "" {
			t.Fatal("expected non-empty hash")
		}
		if hash == password {
			t.Fatal("hash should not equal plaintext password")
		}
		if !CheckPassword(hash, password) {
			t.Error("CheckPassword should return true for correct password")
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		hash, err := HashPassword("CorrectPassword1")
		if err != nil {
			t.Fatalf("HashPassword failed: %v", err)
		}
		if CheckPassword(hash, "WrongPassword1") {
			t.Error("CheckPassword should return false for wrong password")
		}
	})

	t.Run("different hashes for same password", func(t *testing.T) {
		hash1, err := HashPassword("SamePass99")
		if err != nil {
			t.Fatalf("HashPassword failed: %v", err)
		}
		hash2, err := HashPassword("SamePass99")
		if err != nil {
			t.Fatalf("HashPassword failed: %v", err)
		}
		if hash1 == hash2 {
			t.Error("bcrypt should produce different hashes for the same password (different salts)")
		}
		// Both should still verify correctly.
		if !CheckPassword(hash1, "SamePass99") {
			t.Error("hash1 should verify")
		}
		if !CheckPassword(hash2, "SamePass99") {
			t.Error("hash2 should verify")
		}
	})
}
