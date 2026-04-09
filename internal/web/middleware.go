package web

import "net/http"

// securityHeaders wraps a handler with standard security response headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// CSP: 'unsafe-inline' is currently required for both script-src
		// and style-src because the HTML templates contain ~160 inline
		// onclick="..." handlers and ~180 inline style="..." attributes
		// across 15 files. This weakens XSS defence: any successful
		// injection into a template that isn't html/template-escaped
		// can execute inline. Hardening this to a nonce-based CSP
		// requires refactoring every onclick to addEventListener and
		// migrating inline styles to classes — tracked as a follow-up
		// issue on the Gitea tracker. For now, the mitigations are
		// html/template auto-escaping, the webhook/feed/OIDC fixes
		// landed alongside this comment, and X-Frame-Options: DENY.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'")
		next.ServeHTTP(w, r)
	})
}
