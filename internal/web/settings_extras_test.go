package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

type scannerUpdater struct {
	failingUpdater
	available bool
}

func (s scannerUpdater) ScannerAvailable() bool { return s.available }

func TestApiScannerSettings_ReportsAvailability(t *testing.T) {
	for _, avail := range []bool{true, false} {
		srv := &Server{deps: Dependencies{SettingsStore: newMockSettingsStore(), Updater: scannerUpdater{available: avail}}}
		w := httptest.NewRecorder()
		srv.apiScannerSettings(w, httptest.NewRequest(http.MethodGet, "/api/settings/scanner", nil))
		var got map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		want := "false"
		if avail {
			want = "true"
		}
		if got["scanner_available"] != want {
			t.Errorf("scanner_available = %q, want %s", got["scanner_available"], want)
		}
	}
	// An updater that cannot say leaves the field out, so the page shows no warning.
	srv := &Server{deps: Dependencies{SettingsStore: newMockSettingsStore(), Updater: failingUpdater{}}}
	w := httptest.NewRecorder()
	srv.apiScannerSettings(w, httptest.NewRequest(http.MethodGet, "/api/settings/scanner", nil))
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if _, ok := got["scanner_available"]; ok {
		t.Errorf("scanner_available reported without an updater that can tell")
	}
}

func TestSettingsPage_NoLongerSaysTrivyOnTheHost(t *testing.T) {
	b, _ := staticFS.ReadFile("static/settings.html")
	if strings.Contains(string(b), "Trivy binary on the host") {
		t.Error("settings.html still describes Trivy as on the host")
	}
}

func TestApiGetRegistries_ShowsRejectedCredentials(t *testing.T) {
	rejected := map[string]time.Time{"registry.example": time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)}
	old := rejectedCredentials
	rejectedCredentials = func() map[string]time.Time { return rejected }
	defer func() { rejectedCredentials = old }()

	srv := &Server{deps: Dependencies{RegistryCredentials: &memCreds{creds: []RegistryCredential{{ID: "1", Registry: "registry.example", Username: "u", Secret: "supersecret"}}}, Log: nopLogger()}}
	w := httptest.NewRecorder()
	srv.apiGetRegistryCredentials(w, httptest.NewRequest(http.MethodGet, "/api/settings/registries", nil))
	var got map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["registry.example"]["credential_rejected_at"] == nil {
		t.Errorf("rejected credential not reported: %s", w.Body.String())
	}
}

func TestCloudAuth_SaveReadAndKeepSecrets(t *testing.T) {
	ss := newMockSettingsStore()
	srv := &Server{deps: Dependencies{SettingsStore: ss, Log: nopLogger()}}
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.apiSaveCloudAuth(w, httptest.NewRequest(http.MethodPost, "/api/settings/cloud-auth", strings.NewReader(body)))
		return w
	}
	if w := post(`{"ecr":{"region":"eu-west-2","access_key":"AKIA123","secret_key":"TOP-SECRET-KEY","account_id":"1234"}}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(ss.data[store.SettingCloudAuthECR], "TOP-SECRET-KEY") {
		t.Fatalf("secret not stored: %q", ss.data[store.SettingCloudAuthECR])
	}

	w := httptest.NewRecorder()
	srv.apiGetCloudAuth(w, httptest.NewRequest(http.MethodGet, "/api/settings/cloud-auth", nil))
	if strings.Contains(w.Body.String(), "TOP-SECRET-KEY") {
		t.Errorf("secret returned on read: %s", w.Body.String())
	}
	var got map[string]map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["ecr"]["region"] != "eu-west-2" || got["ecr"]["secret_key_set"] != true {
		t.Errorf("ecr read = %v", got["ecr"])
	}

	// Saving again with the secret left blank keeps the stored one.
	if w := post(`{"ecr":{"region":"us-east-1","access_key":"AKIA123","secret_key":"","account_id":"1234"}}`); w.Code != http.StatusOK {
		t.Fatalf("resave: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(ss.data[store.SettingCloudAuthECR], "TOP-SECRET-KEY") || !strings.Contains(ss.data[store.SettingCloudAuthECR], "us-east-1") {
		t.Errorf("blank secret should keep the stored one and take the new region: %q", ss.data[store.SettingCloudAuthECR])
	}

	// Validation, and clearing a provider.
	if w := post(`{"acr":{"tenant_id":"t"}}`); w.Code != http.StatusBadRequest {
		t.Errorf("incomplete ACR config: status %d, want 400", w.Code)
	}
	if w := post(`{"gcr":{"service_account_json":"not json"}}`); w.Code != http.StatusBadRequest {
		t.Errorf("invalid GCR JSON: status %d, want 400", w.Code)
	}
	if w := post(`{"ecr":null}`); w.Code != http.StatusOK || ss.data[store.SettingCloudAuthECR] != "" {
		t.Errorf("clearing ECR: status %d, stored %q", w.Code, ss.data[store.SettingCloudAuthECR])
	}
}

func TestApiSettings_CloudAuthIsASecret(t *testing.T) {
	ss := newMockSettingsStore()
	ss.data[store.SettingCloudAuthECR] = `{"secret_key":"TOP-SECRET-KEY"}`
	srv := &Server{deps: Dependencies{SettingsStore: ss, Config: &mockConfigReader{values: map[string]string{}}, Log: nopLogger()}}
	w := httptest.NewRecorder()
	srv.apiSettings(w, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if strings.Contains(w.Body.String(), "TOP-SECRET-KEY") {
		t.Errorf("GET /api/settings leaks the cloud registry secret: %s", w.Body.String())
	}
}

type memCreds struct{ creds []RegistryCredential }

func (m *memCreds) GetRegistryCredentials() ([]RegistryCredential, error) { return m.creds, nil }
func (m *memCreds) SetRegistryCredentials(c []RegistryCredential) error   { m.creds = c; return nil }

func nopLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestApiVerifierSettings_KeylessSignerFields(t *testing.T) {
	ss := newMockSettingsStore()
	srv := newTestServer(ss)

	save := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.apiVerifierSettingsSave(w, httptest.NewRequest(http.MethodPost, "/api/settings/verifier", strings.NewReader(body)))
		return w
	}
	w := save(`{"mode":"enforce","keyless":true,"identity":"https://github.com/acme/app/.github/workflows/release.yml@refs/tags/v1","oidc_issuer":"https://token.actions.githubusercontent.com","identity_regexp":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if ss.data["cosign_identity"] == "" || ss.data["cosign_oidc_issuer"] != "https://token.actions.githubusercontent.com" || ss.data["cosign_identity_regexp"] != "true" {
		t.Errorf("signer settings not saved: %v", ss.data)
	}

	r := httptest.NewRecorder()
	srv.apiVerifierSettings(r, httptest.NewRequest(http.MethodGet, "/api/settings/verifier", nil))
	var got map[string]string
	_ = json.Unmarshal(r.Body.Bytes(), &got)
	if got["identity"] == "" || got["oidc_issuer"] == "" || got["identity_regexp"] != "true" || got["identity_source"] != "saved" {
		t.Errorf("read = %v", got)
	}

	// An omitted field is left alone; an empty one clears it.
	save(`{"mode":"enforce"}`)
	if ss.data["cosign_identity"] == "" {
		t.Errorf("an omitted identity was cleared")
	}
	save(`{"identity":""}`)
	if ss.data["cosign_identity"] != "" {
		t.Errorf("an empty identity was not cleared")
	}
	if w := save(`{"identity":"` + strings.Repeat("a", 2000) + `"}`); w.Code != http.StatusBadRequest {
		t.Errorf("oversized identity: status %d, want 400", w.Code)
	}
}

func TestSettingsPage_HasKeylessSignerFields(t *testing.T) {
	b, _ := staticFS.ReadFile("static/settings.html")
	for _, id := range []string{`id="cosign-identity"`, `id="cosign-identity-regexp"`, `id="cosign-oidc-issuer"`, `id="cosign-identity-source"`} {
		if !strings.Contains(string(b), id) {
			t.Errorf("settings.html lacks %s", id)
		}
	}
	if strings.Count(string(b), "data-keyless-only") != 3 {
		t.Errorf("the three signer rows should be shown only in keyless mode")
	}
}
