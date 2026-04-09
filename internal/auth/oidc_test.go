package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestResolveRoleFromGroups(t *testing.T) {
	tests := []struct {
		name        string
		groups      []string
		mappings    map[string]string
		defaultRole string
		expected    string
	}{
		// Empty inputs
		{
			name:        "empty groups returns defaultRole",
			groups:      []string{},
			mappings:    map[string]string{"admins": RoleAdminID},
			defaultRole: RoleViewerID,
			expected:    RoleViewerID,
		},
		{
			name:        "empty mappings returns defaultRole",
			groups:      []string{"admins", "operators"},
			mappings:    map[string]string{},
			defaultRole: RoleOperatorID,
			expected:    RoleOperatorID,
		},
		{
			name:        "nil mappings returns defaultRole",
			groups:      []string{"admins"},
			mappings:    nil,
			defaultRole: RoleViewerID,
			expected:    RoleViewerID,
		},

		// No matching groups
		{
			name:        "no matching groups returns defaultRole",
			groups:      []string{"other", "staff"},
			mappings:    map[string]string{"admins": RoleAdminID, "ops": RoleOperatorID},
			defaultRole: RoleViewerID,
			expected:    RoleViewerID,
		},

		// Single matches
		{
			name:        "single admin group match returns admin",
			groups:      []string{"admins"},
			mappings:    map[string]string{"admins": RoleAdminID},
			defaultRole: RoleViewerID,
			expected:    RoleAdminID,
		},
		{
			name:        "single operator group match returns operator",
			groups:      []string{"ops"},
			mappings:    map[string]string{"ops": RoleOperatorID},
			defaultRole: RoleViewerID,
			expected:    RoleOperatorID,
		},
		{
			name:        "single viewer group match returns viewer (beats lower priority default)",
			groups:      []string{"staff"},
			mappings:    map[string]string{"staff": RoleViewerID},
			defaultRole: "", // empty defaultRole has priority 0, so viewer(1) beats it
			expected:    RoleViewerID,
		},

		// Priority ordering tests
		{
			name:        "admin beats operator when both match",
			groups:      []string{"ops", "admins"},
			mappings:    map[string]string{"ops": RoleOperatorID, "admins": RoleAdminID},
			defaultRole: RoleViewerID,
			expected:    RoleAdminID,
		},
		{
			name:        "admin beats operator (reverse order)",
			groups:      []string{"admins", "ops"},
			mappings:    map[string]string{"ops": RoleOperatorID, "admins": RoleAdminID},
			defaultRole: RoleViewerID,
			expected:    RoleAdminID,
		},
		{
			name:        "operator beats viewer when both match",
			groups:      []string{"staff", "ops"},
			mappings:    map[string]string{"ops": RoleOperatorID, "staff": RoleViewerID},
			defaultRole: RoleViewerID,
			expected:    RoleOperatorID,
		},
		{
			name:        "operator beats viewer (reverse order)",
			groups:      []string{"ops", "staff"},
			mappings:    map[string]string{"ops": RoleOperatorID, "staff": RoleViewerID},
			defaultRole: RoleViewerID,
			expected:    RoleOperatorID,
		},
		{
			name:        "admin beats operator and viewer",
			groups:      []string{"ops", "staff", "admins"},
			mappings:    map[string]string{"ops": RoleOperatorID, "staff": RoleViewerID, "admins": RoleAdminID},
			defaultRole: RoleViewerID,
			expected:    RoleAdminID,
		},

		// Unknown role IDs in mappings
		{
			name:        "unknown role ID in mapping is skipped, defaultRole returned",
			groups:      []string{"unknown"},
			mappings:    map[string]string{"unknown": "superuser"},
			defaultRole: RoleViewerID,
			expected:    RoleViewerID,
		},
		{
			name:        "unknown role ID skipped, valid match used",
			groups:      []string{"unknown", "ops"},
			mappings:    map[string]string{"unknown": "superuser", "ops": RoleOperatorID},
			defaultRole: RoleViewerID,
			expected:    RoleOperatorID,
		},

		// Default role is used when no match
		{
			name:        "admin as defaultRole used when no match",
			groups:      []string{"other"},
			mappings:    map[string]string{"admins": RoleAdminID},
			defaultRole: RoleAdminID,
			expected:    RoleAdminID,
		},
		{
			name:        "operator as defaultRole used when no match",
			groups:      []string{"other"},
			mappings:    map[string]string{"admins": RoleAdminID},
			defaultRole: RoleOperatorID,
			expected:    RoleOperatorID,
		},

		// Edge cases
		{
			name:        "defaultRole with higher priority than matched role prevents downgrade",
			groups:      []string{"staff"},
			mappings:    map[string]string{"staff": RoleViewerID},
			defaultRole: RoleAdminID,
			expected:    RoleAdminID, // defaultRole admin(3) > matched viewer(1), so admin wins
		},
		{
			name:        "multiple matches all below defaultRole priority",
			groups:      []string{"staff1", "staff2"},
			mappings:    map[string]string{"staff1": RoleViewerID, "staff2": RoleViewerID},
			defaultRole: RoleAdminID,
			expected:    RoleAdminID, // defaultRole admin(3) beats viewer(1)
		},
		{
			name:        "many groups with one admin match",
			groups:      []string{"users", "staff", "admins", "guests"},
			mappings:    map[string]string{"users": RoleViewerID, "admins": RoleAdminID},
			defaultRole: RoleViewerID,
			expected:    RoleAdminID,
		},
		{
			name:        "case sensitive group matching",
			groups:      []string{"Admins"},
			mappings:    map[string]string{"admins": RoleAdminID},
			defaultRole: RoleViewerID,
			expected:    RoleViewerID, // "Admins" != "admins", no match
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveRoleFromGroups(tt.groups, tt.mappings, tt.defaultRole)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestGenerateOIDCState(t *testing.T) {
	t.Run("returns 32-char hex string (16 bytes)", func(t *testing.T) {
		state, err := GenerateOIDCState()
		if err != nil {
			t.Fatalf("GenerateOIDCState failed: %v", err)
		}
		if len(state) != 32 {
			t.Errorf("expected 32-char hex string, got %d chars", len(state))
		}
		// Verify it's valid hex
		if _, err := hex.DecodeString(state); err != nil {
			t.Errorf("state is not valid hex: %v", err)
		}
	})

	t.Run("two calls produce different values", func(t *testing.T) {
		state1, err1 := GenerateOIDCState()
		if err1 != nil {
			t.Fatalf("first GenerateOIDCState failed: %v", err1)
		}
		state2, err2 := GenerateOIDCState()
		if err2 != nil {
			t.Fatalf("second GenerateOIDCState failed: %v", err2)
		}
		if state1 == state2 {
			t.Error("two generated state values should not be identical")
		}
	})

	t.Run("no error on happy path", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			_, err := GenerateOIDCState()
			if err != nil {
				t.Fatalf("GenerateOIDCState failed on iteration %d: %v", i, err)
			}
		}
	})

	t.Run("decodes to exactly 16 bytes", func(t *testing.T) {
		state, err := GenerateOIDCState()
		if err != nil {
			t.Fatalf("GenerateOIDCState failed: %v", err)
		}
		decoded, err := hex.DecodeString(state)
		if err != nil {
			t.Fatalf("failed to decode state: %v", err)
		}
		if len(decoded) != 16 {
			t.Errorf("expected 16 bytes after decoding, got %d", len(decoded))
		}
	})
}

func TestGenerateOIDCNonce(t *testing.T) {
	t.Run("returns 64-char hex string (32 bytes)", func(t *testing.T) {
		nonce, err := GenerateOIDCNonce()
		if err != nil {
			t.Fatalf("GenerateOIDCNonce failed: %v", err)
		}
		if len(nonce) != 64 {
			t.Errorf("expected 64-char hex string, got %d chars", len(nonce))
		}
		if _, err := hex.DecodeString(nonce); err != nil {
			t.Errorf("nonce is not valid hex: %v", err)
		}
	})

	t.Run("two calls produce different values", func(t *testing.T) {
		a, err := GenerateOIDCNonce()
		if err != nil {
			t.Fatalf("first GenerateOIDCNonce failed: %v", err)
		}
		b, err := GenerateOIDCNonce()
		if err != nil {
			t.Fatalf("second GenerateOIDCNonce failed: %v", err)
		}
		if a == b {
			t.Error("two generated nonces should not be identical")
		}
	})
}

func TestGeneratePKCEVerifier(t *testing.T) {
	t.Run("returns 43-char base64url string", func(t *testing.T) {
		verifier, err := GeneratePKCEVerifier()
		if err != nil {
			t.Fatalf("GeneratePKCEVerifier failed: %v", err)
		}
		// RFC 7636: 32 random bytes base64url-encoded without padding = 43 chars
		if len(verifier) != 43 {
			t.Errorf("expected 43-char verifier, got %d chars", len(verifier))
		}
		if _, err := base64.RawURLEncoding.DecodeString(verifier); err != nil {
			t.Errorf("verifier is not valid base64url: %v", err)
		}
	})

	t.Run("two calls produce different values", func(t *testing.T) {
		a, err := GeneratePKCEVerifier()
		if err != nil {
			t.Fatalf("first GeneratePKCEVerifier failed: %v", err)
		}
		b, err := GeneratePKCEVerifier()
		if err != nil {
			t.Fatalf("second GeneratePKCEVerifier failed: %v", err)
		}
		if a == b {
			t.Error("two generated verifiers should not be identical")
		}
	})
}

func TestPKCEChallengeFromVerifier(t *testing.T) {
	t.Run("matches RFC 7636 S256 test vector", func(t *testing.T) {
		// RFC 7636 Appendix B.
		verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		expected := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
		got := PKCEChallengeFromVerifier(verifier)
		if got != expected {
			t.Errorf("challenge mismatch:\n  verifier: %q\n  expected: %q\n  got:      %q",
				verifier, expected, got)
		}
	})

	t.Run("is deterministic", func(t *testing.T) {
		verifier, err := GeneratePKCEVerifier()
		if err != nil {
			t.Fatalf("GeneratePKCEVerifier failed: %v", err)
		}
		a := PKCEChallengeFromVerifier(verifier)
		b := PKCEChallengeFromVerifier(verifier)
		if a != b {
			t.Error("PKCEChallengeFromVerifier must be deterministic for the same input")
		}
	})

	t.Run("equals manual SHA256 + base64url", func(t *testing.T) {
		verifier := "test-verifier-12345"
		sum := sha256.Sum256([]byte(verifier))
		expected := base64.RawURLEncoding.EncodeToString(sum[:])
		got := PKCEChallengeFromVerifier(verifier)
		if got != expected {
			t.Errorf("challenge mismatch: expected %q, got %q", expected, got)
		}
	})
}
