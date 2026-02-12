package auth

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGenerateSessionToken(t *testing.T) {
	t.Run("returns 64-char hex string", func(t *testing.T) {
		token, err := GenerateSessionToken()
		if err != nil {
			t.Fatalf("GenerateSessionToken failed: %v", err)
		}
		if len(token) != 64 {
			t.Errorf("expected 64 chars, got %d", len(token))
		}
		// Verify it's valid hex.
		if _, err := hex.DecodeString(token); err != nil {
			t.Errorf("token is not valid hex: %v", err)
		}
	})

	t.Run("tokens are unique", func(t *testing.T) {
		token1, err := GenerateSessionToken()
		if err != nil {
			t.Fatalf("GenerateSessionToken failed: %v", err)
		}
		token2, err := GenerateSessionToken()
		if err != nil {
			t.Fatalf("GenerateSessionToken failed: %v", err)
		}
		if token1 == token2 {
			t.Error("two generated tokens should not be identical")
		}
	})
}

func TestSetSessionCookie(t *testing.T) {
	t.Run("sets correct cookie attributes", func(t *testing.T) {
		w := httptest.NewRecorder()
		expiry := time.Now().Add(24 * time.Hour)
		SetSessionCookie(w, "test-token-value", expiry, true)

		resp := w.Result()
		cookies := resp.Cookies()
		if len(cookies) != 1 {
			t.Fatalf("expected 1 cookie, got %d", len(cookies))
		}
		c := cookies[0]
		if c.Name != SessionCookieName {
			t.Errorf("expected cookie name %q, got %q", SessionCookieName, c.Name)
		}
		if c.Value != "test-token-value" {
			t.Errorf("expected cookie value %q, got %q", "test-token-value", c.Value)
		}
		if c.Path != "/" {
			t.Errorf("expected path '/', got %q", c.Path)
		}
		if !c.HttpOnly {
			t.Error("expected HttpOnly to be true")
		}
		if !c.Secure {
			t.Error("expected Secure to be true when secure=true")
		}
		if c.SameSite != http.SameSiteLaxMode {
			t.Errorf("expected SameSiteLaxMode, got %v", c.SameSite)
		}
	})

	t.Run("secure false", func(t *testing.T) {
		w := httptest.NewRecorder()
		SetSessionCookie(w, "token", time.Now().Add(time.Hour), false)
		resp := w.Result()
		c := resp.Cookies()[0]
		if c.Secure {
			t.Error("expected Secure to be false when secure=false")
		}
	})
}

func TestClearSessionCookie(t *testing.T) {
	t.Run("sets MaxAge -1", func(t *testing.T) {
		w := httptest.NewRecorder()
		ClearSessionCookie(w, false)

		resp := w.Result()
		cookies := resp.Cookies()
		if len(cookies) != 1 {
			t.Fatalf("expected 1 cookie, got %d", len(cookies))
		}
		c := cookies[0]
		if c.Name != SessionCookieName {
			t.Errorf("expected cookie name %q, got %q", SessionCookieName, c.Name)
		}
		if c.Value != "" {
			t.Errorf("expected empty cookie value, got %q", c.Value)
		}
		if c.MaxAge != -1 {
			t.Errorf("expected MaxAge -1, got %d", c.MaxAge)
		}
	})
}

func TestGetSessionToken(t *testing.T) {
	t.Run("extracts from request cookie", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(&http.Cookie{
			Name:  SessionCookieName,
			Value: "my-session-token",
		})
		got := GetSessionToken(req)
		if got != "my-session-token" {
			t.Errorf("expected %q, got %q", "my-session-token", got)
		}
	})

	t.Run("returns empty string when no cookie", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		got := GetSessionToken(req)
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("returns empty string for wrong cookie name", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(&http.Cookie{
			Name:  "wrong_cookie",
			Value: "some-value",
		})
		got := GetSessionToken(req)
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})
}
