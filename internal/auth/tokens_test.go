package auth

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestGenerateAPIToken(t *testing.T) {
	t.Run("returns stk_ prefix and non-empty hash", func(t *testing.T) {
		plaintext, hash, err := GenerateAPIToken()
		if err != nil {
			t.Fatalf("GenerateAPIToken failed: %v", err)
		}
		if !strings.HasPrefix(plaintext, TokenPrefix) {
			t.Errorf("expected token to start with %q, got %q", TokenPrefix, plaintext)
		}
		if hash == "" {
			t.Error("expected non-empty hash")
		}
		if len(hash) != 64 {
			t.Errorf("expected 64-char SHA-256 hex hash, got %d chars", len(hash))
		}
	})

	t.Run("tokens are unique", func(t *testing.T) {
		p1, h1, _ := GenerateAPIToken()
		p2, h2, _ := GenerateAPIToken()
		if p1 == p2 {
			t.Error("two generated tokens should not be identical")
		}
		if h1 == h2 {
			t.Error("two generated hashes should not be identical")
		}
	})

	t.Run("hash matches HashToken of plaintext", func(t *testing.T) {
		plaintext, hash, err := GenerateAPIToken()
		if err != nil {
			t.Fatalf("GenerateAPIToken failed: %v", err)
		}
		if HashToken(plaintext) != hash {
			t.Error("hash returned by GenerateAPIToken should match HashToken(plaintext)")
		}
	})
}

func TestGenerateTokenID(t *testing.T) {
	t.Run("returns 16-char hex string", func(t *testing.T) {
		id, err := GenerateTokenID()
		if err != nil {
			t.Fatalf("GenerateTokenID failed: %v", err)
		}
		if len(id) != 16 {
			t.Errorf("expected 16 chars, got %d", len(id))
		}
		if _, err := hex.DecodeString(id); err != nil {
			t.Errorf("token ID is not valid hex: %v", err)
		}
	})

	t.Run("IDs are unique", func(t *testing.T) {
		id1, _ := GenerateTokenID()
		id2, _ := GenerateTokenID()
		if id1 == id2 {
			t.Error("two generated token IDs should not be identical")
		}
	})
}

func TestHashToken(t *testing.T) {
	t.Run("is deterministic", func(t *testing.T) {
		token := "stk_some-test-token"
		h1 := HashToken(token)
		h2 := HashToken(token)
		if h1 != h2 {
			t.Error("HashToken should return the same value for the same input")
		}
	})

	t.Run("different inputs produce different hashes", func(t *testing.T) {
		h1 := HashToken("token-a")
		h2 := HashToken("token-b")
		if h1 == h2 {
			t.Error("different tokens should produce different hashes")
		}
	})

	t.Run("returns 64-char hex string", func(t *testing.T) {
		h := HashToken("anything")
		if len(h) != 64 {
			t.Errorf("expected 64 chars, got %d", len(h))
		}
		if _, err := hex.DecodeString(h); err != nil {
			t.Errorf("hash is not valid hex: %v", err)
		}
	})
}

func TestExtractBearerToken(t *testing.T) {
	t.Run("extracts from Bearer header", func(t *testing.T) {
		got := ExtractBearerToken("Bearer my-token-123")
		if got != "my-token-123" {
			t.Errorf("expected %q, got %q", "my-token-123", got)
		}
	})

	t.Run("returns empty for missing prefix", func(t *testing.T) {
		got := ExtractBearerToken("Basic dXNlcjpwYXNz")
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("returns empty for empty string", func(t *testing.T) {
		got := ExtractBearerToken("")
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("trims whitespace from token", func(t *testing.T) {
		got := ExtractBearerToken("Bearer  token-with-spaces  ")
		if got != "token-with-spaces" {
			t.Errorf("expected %q, got %q", "token-with-spaces", got)
		}
	})

	t.Run("case sensitive prefix", func(t *testing.T) {
		got := ExtractBearerToken("bearer my-token")
		if got != "" {
			t.Errorf("expected empty string for lowercase 'bearer', got %q", got)
		}
	})
}
