package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
)

func TestApiOIDCLinkStart_NeedsSessionAndProvider(t *testing.T) {
	srv := newMFATestServer()
	user := createTestUser(srv.deps.Auth, "alice", "Str0ngP@ssword!")

	w := httptest.NewRecorder()
	srv.apiOIDCLinkStart(w, httptest.NewRequest(http.MethodPost, "/api/auth/oidc/link", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no session: status = %d, want 401", w.Code)
	}

	w = httptest.NewRecorder()
	r := reqWithAuthContext(httptest.NewRequest(http.MethodPost, "/api/auth/oidc/link", nil), &user)
	srv.apiOIDCLinkStart(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("no SSO configured: status = %d, want 404", w.Code)
	}
}

func TestApiOIDCUnlink(t *testing.T) {
	srv := newMFATestServer()
	user := createTestUser(srv.deps.Auth, "alice", "Str0ngP@ssword!")
	user.OIDCIssuer, user.OIDCSubject = "https://idp.example", "sub-1"
	_ = srv.deps.Auth.Users.UpdateUser(user)

	r := reqWithAuthContext(httptest.NewRequest(http.MethodPost, "/api/auth/users/"+user.ID+"/oidc-unlink", nil), &user)
	r.SetPathValue("id", user.ID)
	w := httptest.NewRecorder()
	srv.apiOIDCUnlink(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	got, _ := srv.deps.Auth.Users.GetUser(user.ID)
	if got.OIDCSubject != "" || got.OIDCIssuer != "" {
		t.Errorf("link not removed: %+v", got)
	}
}

func TestApiListUsers_ReportsSSOLink(t *testing.T) {
	srv := newMFATestServer()
	user := createTestUser(srv.deps.Auth, "alice", "Str0ngP@ssword!")
	user.OIDCIssuer, user.OIDCSubject = "https://idp.example", "sub-1"
	_ = srv.deps.Auth.Users.UpdateUser(user)

	w := httptest.NewRecorder()
	srv.apiListUsers(w, reqWithAuthContext(httptest.NewRequest(http.MethodGet, "/api/auth/users", nil), &user))
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if rows[0]["sso_linked"] != true {
		t.Errorf("sso_linked = %v, want true", rows[0]["sso_linked"])
	}
	if strings.Contains(w.Body.String(), "sub-1") {
		t.Errorf("the subject must not be returned")
	}
}

func TestOIDCErrorTexts(t *testing.T) {
	if got := oidcLoginErrorSlug(auth.ErrOIDCAccountExists); got != "sso_account_exists" {
		t.Errorf("slug = %q", got)
	}
	if !strings.Contains(loginErrorText("sso_account_exists"), "already exists") {
		t.Errorf("clash text does not say the account exists")
	}
	if loginErrorText("sso_failed") == "sso_failed" {
		t.Errorf("sso_failed shown as a raw slug")
	}
	if loginErrorText("Invalid username or password") != "Invalid username or password" {
		t.Errorf("other messages must pass through")
	}
}

func TestAccountPage_ShowsLinkButtonOnlyWhenUnlinked(t *testing.T) {
	out := func(linked bool) string {
		srv := &Server{}
		srv.parseTemplates()
		var b strings.Builder
		data := map[string]any{
			"CurrentUser": &auth.User{Username: "alice"}, "AuthEnabled": true, "SSOAvailable": true, "SSOLinked": linked,
			"SSOMessage": accountSSOMessage("linked"), "Sessions": []auth.Session{}, "APITokens": []auth.APIToken{},
			"Passkeys": []auth.WebAuthnCredential{},
		}
		if err := srv.tmpl.ExecuteTemplate(&b, "account.html", data); err != nil {
			t.Fatalf("render: %v", err)
		}
		return b.String()
	}
	if o := out(false); !strings.Contains(o, "sso-link-btn") || !strings.Contains(o, "now linked") {
		t.Errorf("unlinked account should offer the link button and show the message")
	}
	if o := out(true); strings.Contains(o, "sso-link-btn") {
		t.Errorf("linked account should not offer the link button")
	}
}
