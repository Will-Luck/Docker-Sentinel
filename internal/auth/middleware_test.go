package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetRequestContext(t *testing.T) {
	t.Run("returns nil from empty context", func(t *testing.T) {
		ctx := context.Background()
		rc := GetRequestContext(ctx)
		if rc != nil {
			t.Errorf("expected nil, got %v", rc)
		}
	})

	t.Run("returns RequestContext when set", func(t *testing.T) {
		rc := &RequestContext{
			User:        &User{ID: "u1", Username: "testuser"},
			Permissions: []Permission{PermContainersView},
		}
		ctx := context.WithValue(context.Background(), ContextKey, rc)
		got := GetRequestContext(ctx)
		if got == nil {
			t.Fatal("expected non-nil RequestContext")
		}
		if got.User.ID != "u1" {
			t.Errorf("expected user ID %q, got %q", "u1", got.User.ID)
		}
	})

	t.Run("returns nil for wrong type in context", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ContextKey, "not a RequestContext")
		rc := GetRequestContext(ctx)
		if rc != nil {
			t.Errorf("expected nil for wrong type, got %v", rc)
		}
	})
}

func TestIsAPIRequest(t *testing.T) {
	t.Run("api path prefix", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/containers", nil)
		if !isAPIRequest(req) {
			t.Error("expected /api/ path to be detected as API request")
		}
	})

	t.Run("non-api path", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/dashboard", nil)
		if isAPIRequest(req) {
			t.Error("expected /dashboard to not be detected as API request")
		}
	})

	t.Run("Accept application/json header", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/dashboard", nil)
		req.Header.Set("Accept", "application/json")
		if !isAPIRequest(req) {
			t.Error("expected Accept: application/json to be detected as API request")
		}
	})

	t.Run("Accept html header on non-api path", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/dashboard", nil)
		req.Header.Set("Accept", "text/html")
		if isAPIRequest(req) {
			t.Error("expected text/html Accept header to not be an API request")
		}
	})

	t.Run("api path nested", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/users", nil)
		if !isAPIRequest(req) {
			t.Error("expected /api/v1/users to be detected as API request")
		}
	})
}

func TestAuthMiddleware(t *testing.T) {
	t.Run("auth disabled injects synthetic admin context", func(t *testing.T) {
		svc := newTestService(false)

		var capturedRC *RequestContext
		handler := AuthMiddleware(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedRC = GetRequestContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/dashboard", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if capturedRC == nil {
			t.Fatal("expected RequestContext to be set")
		}
		if capturedRC.User == nil || capturedRC.User.Username != "admin" {
			t.Error("expected synthetic admin user")
		}
		if capturedRC.AuthEnabled {
			t.Error("expected AuthEnabled to be false")
		}
		if len(capturedRC.Permissions) != len(AllPermissions()) {
			t.Errorf("expected all permissions, got %d", len(capturedRC.Permissions))
		}
	})

	t.Run("auth enabled with no credentials redirects browser to login", func(t *testing.T) {
		svc := newTestService(true)

		handler := AuthMiddleware(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/dashboard", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 redirect, got %d", w.Code)
		}
		location := w.Header().Get("Location")
		if location != "/login" {
			t.Errorf("expected redirect to /login, got %q", location)
		}
	})

	t.Run("auth enabled with no credentials returns 401 for API request", func(t *testing.T) {
		svc := newTestService(true)

		handler := AuthMiddleware(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/api/containers", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", w.Code)
		}
	})

	t.Run("auth enabled with valid session passes through", func(t *testing.T) {
		svc := newTestService(true)

		// Create a user and session in the mock stores.
		hash, _ := HashPassword("TestPass1")
		user := User{
			ID:           "user1",
			Username:     "testuser",
			PasswordHash: hash,
			RoleID:       RoleAdminID,
		}
		_ = svc.Users.CreateUser(user)

		session := Session{
			Token:     "valid-session-token",
			UserID:    "user1",
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(24 * time.Hour),
		}
		_ = svc.Sessions.CreateSession(session)

		var capturedRC *RequestContext
		handler := AuthMiddleware(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedRC = GetRequestContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/dashboard", nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "valid-session-token"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if capturedRC == nil {
			t.Fatal("expected RequestContext to be set")
		}
		if capturedRC.User.ID != "user1" {
			t.Errorf("expected user ID %q, got %q", "user1", capturedRC.User.ID)
		}
		if !capturedRC.AuthEnabled {
			t.Error("expected AuthEnabled to be true")
		}
	})

	t.Run("auth enabled with invalid bearer returns 401", func(t *testing.T) {
		svc := newTestService(true)

		handler := AuthMiddleware(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/api/data", nil)
		req.Header.Set("Authorization", "Bearer invalid-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", w.Code)
		}
	})

	t.Run("auth enabled with valid bearer token passes through", func(t *testing.T) {
		svc := newTestService(true)

		// Create a user.
		hash, _ := HashPassword("TestPass1")
		user := User{
			ID:           "user2",
			Username:     "apiuser",
			PasswordHash: hash,
			RoleID:       RoleAdminID,
		}
		_ = svc.Users.CreateUser(user)

		// Create an API token.
		plaintext, tokenHash, _ := GenerateAPIToken()
		apiToken := APIToken{
			ID:        "tok1",
			Name:      "test-token",
			TokenHash: tokenHash,
			UserID:    "user2",
		}
		_ = svc.Tokens.CreateAPIToken(apiToken)

		var capturedRC *RequestContext
		handler := AuthMiddleware(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedRC = GetRequestContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/api/data", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if capturedRC == nil {
			t.Fatal("expected RequestContext to be set")
		}
		if capturedRC.User.ID != "user2" {
			t.Errorf("expected user ID %q, got %q", "user2", capturedRC.User.ID)
		}
		if capturedRC.APIToken == nil {
			t.Error("expected APIToken to be set in context")
		}
	})
}

func TestCSRFMiddleware(t *testing.T) {
	t.Run("passes GET requests through", func(t *testing.T) {
		called := false
		handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/dashboard", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if !called {
			t.Error("expected handler to be called for GET request")
		}
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("passes HEAD requests through", func(t *testing.T) {
		called := false
		handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
		}))

		req := httptest.NewRequest("HEAD", "/dashboard", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if !called {
			t.Error("expected handler to be called for HEAD request")
		}
	})

	t.Run("passes OPTIONS requests through", func(t *testing.T) {
		called := false
		handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
		}))

		req := httptest.NewRequest("OPTIONS", "/dashboard", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if !called {
			t.Error("expected handler to be called for OPTIONS request")
		}
	})

	t.Run("passes POST with bearer token (API exempt)", func(t *testing.T) {
		called := false
		handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("POST", "/api/action", nil)
		req.Header.Set("Authorization", "Bearer some-api-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if !called {
			t.Error("expected handler to be called for bearer-token POST (CSRF exempt)")
		}
	})

	t.Run("passes POST when auth disabled", func(t *testing.T) {
		called := false
		handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}))

		// Inject a RequestContext with AuthEnabled=false.
		rc := &RequestContext{
			User:        &User{ID: "system", Username: "admin"},
			Permissions: AllPermissions(),
			AuthEnabled: false,
		}
		req := httptest.NewRequest("POST", "/action", nil)
		req = req.WithContext(context.WithValue(req.Context(), ContextKey, rc))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if !called {
			t.Error("expected handler to be called when auth is disabled")
		}
	})

	t.Run("blocks POST without CSRF token", func(t *testing.T) {
		called := false
		handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
		}))

		// Inject auth-enabled context so CSRF check runs.
		rc := &RequestContext{
			User:        &User{ID: "u1"},
			AuthEnabled: true,
		}
		req := httptest.NewRequest("POST", "/action", nil)
		req = req.WithContext(context.WithValue(req.Context(), ContextKey, rc))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if called {
			t.Error("expected handler NOT to be called without CSRF token")
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", w.Code)
		}
	})

	t.Run("passes POST with valid CSRF double-submit", func(t *testing.T) {
		called := false
		handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}))

		rc := &RequestContext{
			User:        &User{ID: "u1"},
			AuthEnabled: true,
		}
		req := httptest.NewRequest("POST", "/action", nil)
		req = req.WithContext(context.WithValue(req.Context(), ContextKey, rc))
		req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "csrf-match"})
		req.Header.Set(CSRFHeaderName, "csrf-match")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if !called {
			t.Error("expected handler to be called with valid CSRF tokens")
		}
	})

	t.Run("blocks DELETE without CSRF", func(t *testing.T) {
		called := false
		handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
		}))

		rc := &RequestContext{
			User:        &User{ID: "u1"},
			AuthEnabled: true,
		}
		req := httptest.NewRequest("DELETE", "/api/resource", nil)
		req.Header.Set("Accept", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), ContextKey, rc))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if called {
			t.Error("expected handler NOT to be called for DELETE without CSRF")
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", w.Code)
		}
	})
}

func TestRequirePermission(t *testing.T) {
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("blocks without RequestContext", func(t *testing.T) {
		handler := RequirePermission(PermContainersView)(okHandler)

		req := httptest.NewRequest("GET", "/containers", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", w.Code)
		}
	})

	t.Run("blocks without required permission", func(t *testing.T) {
		handler := RequirePermission(PermSettingsModify)(okHandler)

		rc := &RequestContext{
			User:        &User{ID: "u1"},
			Permissions: []Permission{PermContainersView, PermLogsView},
			AuthEnabled: true,
		}
		req := httptest.NewRequest("GET", "/settings", nil)
		req = req.WithContext(context.WithValue(req.Context(), ContextKey, rc))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", w.Code)
		}
	})

	t.Run("passes with required permission", func(t *testing.T) {
		handler := RequirePermission(PermContainersView)(okHandler)

		rc := &RequestContext{
			User:        &User{ID: "u1"},
			Permissions: []Permission{PermContainersView, PermLogsView},
			AuthEnabled: true,
		}
		req := httptest.NewRequest("GET", "/containers", nil)
		req = req.WithContext(context.WithValue(req.Context(), ContextKey, rc))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("returns 403 JSON for API request without permission", func(t *testing.T) {
		handler := RequirePermission(PermUsersManage)(okHandler)

		rc := &RequestContext{
			User:        &User{ID: "u1"},
			Permissions: []Permission{PermContainersView},
			AuthEnabled: true,
		}
		req := httptest.NewRequest("GET", "/api/users", nil)
		req = req.WithContext(context.WithValue(req.Context(), ContextKey, rc))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", w.Code)
		}
		body := w.Body.String()
		if body == "" || body[0] != '{' {
			t.Errorf("expected JSON error body for API request, got %q", body)
		}
	})

	t.Run("returns HTML for browser request without permission", func(t *testing.T) {
		handler := RequirePermission(PermUsersManage)(okHandler)

		rc := &RequestContext{
			User:        &User{ID: "u1"},
			Permissions: []Permission{PermContainersView},
			AuthEnabled: true,
		}
		req := httptest.NewRequest("GET", "/users", nil)
		req.Header.Set("Accept", "text/html")
		req = req.WithContext(context.WithValue(req.Context(), ContextKey, rc))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", w.Code)
		}
		body := w.Body.String()
		if len(body) > 0 && body[0] == '{' {
			t.Errorf("expected non-JSON body for browser request, got %q", body)
		}
	})
}

func TestHasPermission(t *testing.T) {
	t.Run("returns true when permission exists", func(t *testing.T) {
		rc := &RequestContext{
			Permissions: []Permission{PermContainersView, PermLogsView},
		}
		if !rc.HasPermission(PermContainersView) {
			t.Error("expected HasPermission to return true")
		}
	})

	t.Run("returns false when permission missing", func(t *testing.T) {
		rc := &RequestContext{
			Permissions: []Permission{PermContainersView},
		}
		if rc.HasPermission(PermUsersManage) {
			t.Error("expected HasPermission to return false")
		}
	})

	t.Run("returns false for empty permissions", func(t *testing.T) {
		rc := &RequestContext{}
		if rc.HasPermission(PermContainersView) {
			t.Error("expected HasPermission to return false for empty permissions")
		}
	})
}
