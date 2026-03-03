package auth

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestGenerateTOTPSecret(t *testing.T) {
	key, err := GenerateTOTPSecret("testuser")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil key")
	}
	if key.Secret() == "" {
		t.Error("expected non-empty secret")
	}
	if key.URL() == "" {
		t.Error("expected non-empty URL")
	}
	if key.Issuer() != totpIssuer {
		t.Errorf("issuer = %q, want %q", key.Issuer(), totpIssuer)
	}
	if key.AccountName() != "testuser" {
		t.Errorf("account = %q, want %q", key.AccountName(), "testuser")
	}
}

func TestValidateTOTPCode(t *testing.T) {
	key, err := GenerateTOTPSecret("testuser")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}

	// Generate a valid code.
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}

	// Valid code should pass.
	if !ValidateTOTPCode(key.Secret(), code) {
		t.Error("expected valid code to pass")
	}

	// Wrong code should fail.
	if ValidateTOTPCode(key.Secret(), "000000") && code != "000000" {
		t.Error("expected wrong code to fail")
	}

	// Empty code should fail.
	if ValidateTOTPCode(key.Secret(), "") {
		t.Error("expected empty code to fail")
	}
}

func TestGenerateRecoveryCodes(t *testing.T) {
	plain, stored, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}

	// Check count.
	if len(plain) != recoveryCodeCount {
		t.Errorf("len(plain) = %d, want %d", len(plain), recoveryCodeCount)
	}
	if len(stored) != recoveryCodeCount {
		t.Errorf("len(stored) = %d, want %d", len(stored), recoveryCodeCount)
	}

	// Check each code has the expected length.
	for i, code := range plain {
		if len(code) != recoveryCodeLen {
			t.Errorf("plain[%d] len = %d, want %d", i, len(code), recoveryCodeLen)
		}
	}

	// Check all codes are unique.
	seen := make(map[string]bool)
	for _, code := range plain {
		if seen[code] {
			t.Errorf("duplicate recovery code: %s", code)
		}
		seen[code] = true
	}

	// Stored values should be SHA-256 hashes of the plain codes.
	for i := range plain {
		if stored[i] != hashRecoveryCode(plain[i]) {
			t.Errorf("stored[%d] = %q, want SHA-256 of plain[%d] = %q", i, stored[i], i, plain[i])
		}
	}
}

func TestValidateRecoveryCode(t *testing.T) {
	// Stored codes are SHA-256 hashes of the plaintext.
	plainCodes := []string{"aabbccdd", "11223344", "deadbeef"}
	storedHashes := make([]string, len(plainCodes))
	for i, c := range plainCodes {
		storedHashes[i] = hashRecoveryCode(c)
	}

	// Matching code should return its index.
	idx := ValidateRecoveryCode("11223344", storedHashes)
	if idx != 1 {
		t.Errorf("ValidateRecoveryCode match: idx = %d, want 1", idx)
	}

	// First code.
	idx = ValidateRecoveryCode("aabbccdd", storedHashes)
	if idx != 0 {
		t.Errorf("ValidateRecoveryCode first: idx = %d, want 0", idx)
	}

	// Last code.
	idx = ValidateRecoveryCode("deadbeef", storedHashes)
	if idx != 2 {
		t.Errorf("ValidateRecoveryCode last: idx = %d, want 2", idx)
	}

	// Non-matching code should return -1.
	idx = ValidateRecoveryCode("notacode", storedHashes)
	if idx != -1 {
		t.Errorf("ValidateRecoveryCode miss: idx = %d, want -1", idx)
	}

	// Empty input should return -1.
	idx = ValidateRecoveryCode("", storedHashes)
	if idx != -1 {
		t.Errorf("ValidateRecoveryCode empty: idx = %d, want -1", idx)
	}

	// Empty stored should return -1.
	idx = ValidateRecoveryCode("aabbccdd", nil)
	if idx != -1 {
		t.Errorf("ValidateRecoveryCode nil stored: idx = %d, want -1", idx)
	}
}
