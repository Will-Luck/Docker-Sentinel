package auth

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenerateCSRFToken(t *testing.T) {
	t.Run("returns 64-char hex string", func(t *testing.T) {
		token, err := GenerateCSRFToken()
		if err != nil {
			t.Fatalf("GenerateCSRFToken failed: %v", err)
		}
		if len(token) != 64 {
			t.Errorf("expected 64 chars, got %d", len(token))
		}
		if _, err := hex.DecodeString(token); err != nil {
			t.Errorf("token is not valid hex: %v", err)
		}
	})

	t.Run("tokens are unique", func(t *testing.T) {
		t1, _ := GenerateCSRFToken()
		t2, _ := GenerateCSRFToken()
		if t1 == t2 {
			t.Error("two generated CSRF tokens should not be identical")
		}
	})
}

func TestSetCSRFCookie(t *testing.T) {
	t.Run("HttpOnly is false", func(t *testing.T) {
		w := httptest.NewRecorder()
		SetCSRFCookie(w, "csrf-token-value", false)

		resp := w.Result()
		cookies := resp.Cookies()
		if len(cookies) != 1 {
			t.Fatalf("expected 1 cookie, got %d", len(cookies))
		}
		c := cookies[0]
		if c.Name != CSRFCookieName {
			t.Errorf("expected cookie name %q, got %q", CSRFCookieName, c.Name)
		}
		if c.Value != "csrf-token-value" {
			t.Errorf("expected cookie value %q, got %q", "csrf-token-value", c.Value)
		}
		if c.HttpOnly {
			t.Error("CSRF cookie must NOT be HttpOnly (JS needs to read it)")
		}
		if c.Path != "/" {
			t.Errorf("expected path '/', got %q", c.Path)
		}
		if c.SameSite != http.SameSiteLaxMode {
			t.Errorf("expected SameSiteLaxMode, got %v", c.SameSite)
		}
	})

	t.Run("secure flag propagated", func(t *testing.T) {
		w := httptest.NewRecorder()
		SetCSRFCookie(w, "token", true)
		resp := w.Result()
		c := resp.Cookies()[0]
		if !c.Secure {
			t.Error("expected Secure to be true")
		}
	})
}

func TestValidateCSRF(t *testing.T) {
	t.Run("matching header and cookie", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", nil)
		req.AddCookie(&http.Cookie{
			Name:  CSRFCookieName,
			Value: "matching-token",
		})
		req.Header.Set(CSRFHeaderName, "matching-token")
		if !ValidateCSRF(req) {
			t.Error("expected validation to pass with matching header and cookie")
		}
	})

	t.Run("mismatched header and cookie", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", nil)
		req.AddCookie(&http.Cookie{
			Name:  CSRFCookieName,
			Value: "cookie-token",
		})
		req.Header.Set(CSRFHeaderName, "different-token")
		if ValidateCSRF(req) {
			t.Error("expected validation to fail with mismatched tokens")
		}
	})

	t.Run("missing cookie", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", nil)
		req.Header.Set(CSRFHeaderName, "some-token")
		if ValidateCSRF(req) {
			t.Error("expected validation to fail with missing cookie")
		}
	})

	t.Run("missing header", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", nil)
		req.AddCookie(&http.Cookie{
			Name:  CSRFCookieName,
			Value: "cookie-token",
		})
		// No header set, no form value.
		if ValidateCSRF(req) {
			t.Error("expected validation to fail with missing header")
		}
	})

	t.Run("empty cookie value", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", nil)
		req.AddCookie(&http.Cookie{
			Name:  CSRFCookieName,
			Value: "",
		})
		req.Header.Set(CSRFHeaderName, "")
		if ValidateCSRF(req) {
			t.Error("expected validation to fail with empty cookie value")
		}
	})

	t.Run("form value fallback", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/submit?csrf_token=form-token", nil)
		req.AddCookie(&http.Cookie{
			Name:  CSRFCookieName,
			Value: "form-token",
		})
		// No header — should fall back to form value.
		if !ValidateCSRF(req) {
			t.Error("expected validation to pass with matching form value fallback")
		}
	})
}
