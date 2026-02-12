package auth

import (
	"context"
	"net/http"
	"strings"
)

// AuthMiddleware checks authentication via session cookie or API bearer token.
// If auth is disabled, injects a synthetic admin context.
// Unauthenticated browser requests are redirected to /login.
// Unauthenticated API requests get 401.
func AuthMiddleware(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check if auth is enabled.
			if !svc.AuthEnabled() {
				// Auth disabled — inject synthetic admin context.
				ctx := context.WithValue(r.Context(), ContextKey, &RequestContext{
					User:        &User{ID: "system", Username: "admin"},
					Permissions: AllPermissions(),
					AuthEnabled: false,
				})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			var rc *RequestContext

			// Try API bearer token first.
			if bearer := ExtractBearerToken(r.Header.Get("Authorization")); bearer != "" {
				rc = svc.ValidateBearerToken(r.Context(), bearer)
				if rc != nil {
					rc.AuthEnabled = true
					ctx := context.WithValue(r.Context(), ContextKey, rc)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				// Invalid bearer token.
				http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
				return
			}

			// Try session cookie.
			if token := GetSessionToken(r); token != "" {
				rc = svc.ValidateSession(r.Context(), token)
				if rc != nil {
					rc.AuthEnabled = true
					// Ensure CSRF cookie is set for browser sessions.
					ensureCSRFCookie(w, r, svc.CookieSecure)
					ctx := context.WithValue(r.Context(), ContextKey, rc)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				// Invalid/expired session — clear the stale cookie.
				ClearSessionCookie(w, svc.CookieSecure)
			}

			// Not authenticated.
			if isAPIRequest(r) {
				http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
		})
	}
}

// CSRFMiddleware validates CSRF tokens on state-changing requests (POST/PUT/DELETE/PATCH).
// Only applies to cookie-authenticated sessions — API bearer tokens are exempt.
func CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Safe methods don't need CSRF validation.
		if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" {
			next.ServeHTTP(w, r)
			return
		}

		// API bearer tokens are exempt from CSRF (they're not cookie-based).
		if ExtractBearerToken(r.Header.Get("Authorization")) != "" {
			next.ServeHTTP(w, r)
			return
		}

		// Auth disabled — skip CSRF.
		rc := GetRequestContext(r.Context())
		if rc != nil && !rc.AuthEnabled {
			next.ServeHTTP(w, r)
			return
		}

		// Validate CSRF double-submit.
		if !ValidateCSRF(r) {
			if isAPIRequest(r) {
				http.Error(w, `{"error":"CSRF validation failed"}`, http.StatusForbidden)
			} else {
				http.Error(w, "CSRF validation failed", http.StatusForbidden)
			}
			return
		}

		next.ServeHTTP(w, r)
	})
}

// RequirePermission returns middleware that checks for a specific permission.
func RequirePermission(perm Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := GetRequestContext(r.Context())
			if rc == nil {
				http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
				return
			}
			if !rc.HasPermission(perm) {
				if isAPIRequest(r) {
					http.Error(w, `{"error":"insufficient permissions"}`, http.StatusForbidden)
				} else {
					http.Error(w, "You don't have permission to access this page.", http.StatusForbidden)
				}
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// GetRequestContext extracts the RequestContext from the request context.
func GetRequestContext(ctx context.Context) *RequestContext {
	rc, _ := ctx.Value(ContextKey).(*RequestContext)
	return rc
}

// isAPIRequest checks if the request is an API call (JSON) vs browser navigation.
func isAPIRequest(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") {
		return true
	}
	return strings.HasPrefix(r.URL.Path, "/api/")
}

// ensureCSRFCookie sets a CSRF cookie if one doesn't already exist.
func ensureCSRFCookie(w http.ResponseWriter, r *http.Request, secure bool) {
	if _, err := r.Cookie(CSRFCookieName); err != nil {
		token, err := GenerateCSRFToken()
		if err != nil {
			return
		}
		SetCSRFCookie(w, token, secure)
	}
}
