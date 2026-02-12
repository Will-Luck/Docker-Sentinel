package auth

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

const (
	CSRFCookieName = "sentinel_csrf"
	CSRFHeaderName = "X-CSRF-Token"
	csrfTokenBytes = 32
)

// GenerateCSRFToken creates a cryptographically random CSRF token.
func GenerateCSRFToken() (string, error) {
	b := make([]byte, csrfTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SetCSRFCookie sets the CSRF double-submit cookie (readable by JS).
func SetCSRFCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false, // JS must read this to send in header
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// ValidateCSRF checks that the CSRF header matches the CSRF cookie (double-submit pattern).
func ValidateCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(CSRFCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	header := r.Header.Get(CSRFHeaderName)
	if header == "" {
		// Also check form value as fallback for HTML form submissions.
		header = r.FormValue("csrf_token")
	}
	return header != "" && header == cookie.Value
}
