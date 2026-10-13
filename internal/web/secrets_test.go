package web

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

func TestApiSettingsDoesNotLeakSecrets(t *testing.T) {
	ss := newMockSettingsStore()
	ss.data["oidc_client_secret"] = "OIDC-SECRET-VALUE"
	ss.data[store.SettingNPMPassword] = "NPM-PASSWORD-VALUE"
	ss.data[store.SettingWebhookSecret] = "WEBHOOK-SECRET-VALUE"
	ss.data["enroll_token"] = "ENROLL-TOKEN-VALUE"
	srv := &Server{deps: Dependencies{SettingsStore: ss, Config: &mockConfigReader{values: map[string]string{}}, Log: slog.Default()}}

	w := httptest.NewRecorder()
	srv.apiSettings(w, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if strings.Contains(w.Body.String(), "-VALUE") {
		t.Errorf("GET /api/settings leaks a secret value: %s", w.Body.String())
	}
	got := getSettings(t, srv)
	for _, k := range []string{"oidc_client_secret", store.SettingNPMPassword, store.SettingWebhookSecret, "enroll_token"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s is present in GET /api/settings", k)
		}
		if got[k+"_set"] != "true" {
			t.Errorf("%s_set = %q, want \"true\"", k, got[k+"_set"])
		}
	}
}

// Every stored settings key that looks like a credential must be on the
// shared secret list, so a new key cannot be forgotten by the settings read
// and the config export.
func TestSecretSettingKeysCoverStoredKeys(t *testing.T) {
	keys := map[string]bool{}
	for k := range validSettingKeys {
		keys[k] = true
	}
	files, err := filepath.Glob("../store/*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob store sources: %v (%d files)", err, len(files))
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Setting") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					keys[v] = true
				}
			}
			return true
		})
	}
	for k := range keys {
		for _, suf := range []string{"_secret", "_password", "_token"} {
			if strings.HasSuffix(k, suf) && !isSensitiveSetting(k) {
				t.Errorf("settings key %q looks like a credential but is not on the secret list", k)
			}
		}
	}
}

func TestConfigExportWithoutSecretsRedactsNPMPassword(t *testing.T) {
	ss := newMockSettingsStore()
	ss.data[store.SettingNPMPassword] = "NPM-PASSWORD-VALUE"
	srv := &Server{deps: Dependencies{SettingsStore: ss, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	w := httptest.NewRecorder()
	srv.apiConfigExport(w, httptest.NewRequest(http.MethodGet, "/api/config/export", nil))
	if strings.Contains(w.Body.String(), "NPM-PASSWORD-VALUE") {
		t.Errorf("export without secrets contains npm_password: %s", w.Body.String())
	}
}

func TestRestoreSecretsKeepsMaskedWebhookHeaders(t *testing.T) {
	saved := notify.Channel{
		ID: "wh1", Type: notify.ProviderWebhook, Name: "hook", Enabled: true,
		Settings: json.RawMessage(`{"url":"https://example.invalid/hook","headers":{"Authorization":"Bearer abc123secret","X-Plain":"old"}}`),
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(notify.MaskSecrets(saved).Settings, &obj); err != nil {
		t.Fatal(err)
	}
	obj["headers"].(map[string]interface{})["X-Plain"] = "edited"
	body, _ := json.Marshal(obj)
	incoming := saved
	incoming.Settings = body

	var ws notify.WebhookSettings
	if err := json.Unmarshal(restoreSecrets(incoming, saved), &ws); err != nil {
		t.Fatal(err)
	}
	if got := ws.Headers["Authorization"]; got != "Bearer abc123secret" {
		t.Errorf("Authorization = %q, want the saved secret", got)
	}
	if got := ws.Headers["X-Plain"]; got != "edited" {
		t.Errorf("X-Plain = %q, want the user's edit", got)
	}
	if ws.URL != "https://example.invalid/hook" {
		t.Errorf("url = %q, want the saved URL", ws.URL)
	}
}

func TestMaskSecretsCoversBasicAuthAndWebhookURL(t *testing.T) {
	ch := notify.Channel{
		ID: "wh1", Type: notify.ProviderWebhook, Name: "hook", Enabled: true,
		Settings: json.RawMessage(`{"url":"https://n8n.example/webhook/3f1c-secret-uuid","headers":{"Authorization":"Basic dXNlcjpwYXNz"}}`),
	}
	out := string(notify.MaskSecrets(ch).Settings)
	for _, leak := range []string{"dXNlcjpwYXNz", "3f1c-secret-uuid"} {
		if strings.Contains(out, leak) {
			t.Errorf("masked webhook settings still contain %q: %s", leak, out)
		}
	}
}

type importNotifyStore struct{ ch []notify.Channel }

func (m *importNotifyStore) GetNotificationChannels() ([]notify.Channel, error) { return m.ch, nil }
func (m *importNotifyStore) SetNotificationChannels(c []notify.Channel) error   { m.ch = c; return nil }

func TestConfigImportOfDefaultExportKeepsChannelSecrets(t *testing.T) {
	settings, _ := json.Marshal(notify.GotifySettings{URL: "http://gotify.example", Token: "RealSecretToken123"})
	ns := &importNotifyStore{ch: []notify.Channel{{ID: "c1", Name: "g", Type: notify.ProviderGotify, Enabled: true, Settings: settings}}}
	srv := &Server{deps: Dependencies{NotifyConfig: ns, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}

	w := httptest.NewRecorder()
	srv.apiConfigExport(w, httptest.NewRequest(http.MethodGet, "/api/config/export", nil))
	w2 := httptest.NewRecorder()
	srv.apiConfigImport(w2, httptest.NewRequest(http.MethodPost, "/api/config/import", bytes.NewReader(w.Body.Bytes())))

	if w2.Code != http.StatusOK || !strings.Contains(w2.Body.String(), `"notifications_imported":1`) {
		t.Fatalf("import status %d, body %s: the channel must be imported, not skipped", w2.Code, w2.Body.String())
	}
	var gs notify.GotifySettings
	_ = json.Unmarshal(ns.ch[0].Settings, &gs)
	if gs.Token != "RealSecretToken123" {
		t.Errorf("stored Gotify token = %q after importing a default export (status %d)", gs.Token, w2.Code)
	}
}

// Environment-sourced values reach GET /api/settings through Config.Values().
// A secret in a URL (userinfo, path or query) must not be returned, and a
// future Values() key that looks like a credential fails here.
func TestApiSettingsDoesNotLeakEnvSecrets(t *testing.T) {
	const planted = "PLANTEDSECRET"
	withSecret := "https://user:" + planted + "@hooks.example/" + planted + "/x?token=" + planted
	// A path is plain on an NPM address, so it is left out of that one.
	t.Setenv("SENTINEL_NPM_URL", "https://user:"+planted+"@npm.example:81?token="+planted)
	for _, k := range []string{"SENTINEL_WEBHOOK_URL", "SENTINEL_GOTIFY_URL", "SENTINEL_SERVER_ADDR", "SENTINEL_HOST", "SENTINEL_CLUSTER_ADVERTISE", "SENTINEL_WEBAUTHN_ORIGINS"} {
		t.Setenv(k, withSecret)
	}
	cfg := config.Load()
	for k := range cfg.Values() {
		for _, suf := range []string{"_SECRET", "_PASSWORD", "_TOKEN"} {
			if strings.HasSuffix(k, suf) {
				t.Errorf("Config.Values() exposes credential-like key %q", k)
			}
		}
	}
	srv := &Server{deps: Dependencies{SettingsStore: newMockSettingsStore(), Config: cfg, Log: slog.Default()}}
	w := httptest.NewRecorder()
	srv.apiSettings(w, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if strings.Contains(w.Body.String(), planted) {
		t.Errorf("GET /api/settings returns a secret from an environment value: %s", w.Body.String())
	}
}

func TestApiWebhookInfoNeverReturnsMoreThanAPrefix(t *testing.T) {
	for _, secret := range []string{"short", "exactly12chr", strings.Repeat("a", 64)} {
		ss := newMockSettingsStore()
		ss.data[store.SettingWebhookSecret] = secret
		srv := &Server{deps: Dependencies{SettingsStore: ss}}
		w := httptest.NewRecorder()
		srv.apiGetWebhookInfo(w, httptest.NewRequest(http.MethodGet, "/api/settings/webhook-info", nil))
		var got map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		shown := strings.ReplaceAll(got["secret"], "*", "")
		if len(shown) > 4 || (len(secret) > 0 && strings.Contains(got["secret"], secret)) {
			t.Errorf("webhook-info returned %q for a %d-char secret, want at most a 4-char prefix", got["secret"], len(secret))
		}
	}
}
