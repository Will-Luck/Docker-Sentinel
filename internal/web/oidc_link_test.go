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
	if loginErrorText("sso_account_exists") != auth.ErrOIDCAccountExists.Error() || !strings.Contains(loginErrorText("sso_account_exists"), "Ask an admin to allow linking") {
		t.Errorf("clash text is not the how-to-link message")
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

func TestApiOIDCAllowLink_AndListShowsPending(t *testing.T) {
	srv := newMFATestServer()
	admin := createTestUser(srv.deps.Auth, "root", "Str0ngP@ssword!")
	other := createTestUser(srv.deps.Auth, "bob", "Str0ngP@ssword!")

	r := reqWithAuthContext(httptest.NewRequest(http.MethodPost, "/x", nil), &admin)
	r.SetPathValue("id", other.ID)
	w := httptest.NewRecorder()
	srv.apiOIDCAllowLink(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	srv.apiListUsers(w, reqWithAuthContext(httptest.NewRequest(http.MethodGet, "/api/auth/users", nil), &admin))
	var rows []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &rows)
	pending := 0
	for _, row := range rows {
		if row["username"] == "bob" && row["sso_link_until"] != nil {
			pending++
		}
		if row["username"] == "root" && row["sso_link_until"] != nil {
			t.Errorf("a user with no window shows one")
		}
	}
	if pending != 1 {
		t.Errorf("pending window not shown on the user's row: %s", w.Body.String())
	}

	r = reqWithAuthContext(httptest.NewRequest(http.MethodPost, "/x", nil), &admin)
	r.SetPathValue("id", "no-such-user")
	w = httptest.NewRecorder()
	srv.apiOIDCAllowLink(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown user: status %d, want 400", w.Code)
	}
}

func TestOIDCSettings_LinkExistingSavedWinsOverEnv(t *testing.T) {
	ss := newMockSettingsStore()
	srv := &Server{deps: Dependencies{SettingsStore: ss, Config: &mockConfigReader{values: map[string]string{"SENTINEL_OIDC_LINK_EXISTING": "true"}}, Log: nopLogger()}}
	read := func() map[string]any {
		w := httptest.NewRecorder()
		srv.apiGetOIDCSettings(w, httptest.NewRequest(http.MethodGet, "/api/settings/oidc", nil))
		var got map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		return got
	}
	if got := read(); got["link_existing"] != true || got["link_existing_source"] != "env" {
		t.Errorf("from env: %v", got)
	}
	ss.data["oidc_link_existing"] = "false"
	if got := read(); got["link_existing"] != false || got["link_existing_source"] != "saved" {
		t.Errorf("saved false: %v", got)
	}
	w := httptest.NewRecorder()
	srv.apiSaveOIDCSettings(w, httptest.NewRequest(http.MethodPost, "/api/settings/oidc", strings.NewReader(`{"enabled":false,"link_existing":true}`)))
	if w.Code != http.StatusOK || ss.data["oidc_link_existing"] != "true" {
		t.Errorf("save: status %d, stored %q", w.Code, ss.data["oidc_link_existing"])
	}
}
