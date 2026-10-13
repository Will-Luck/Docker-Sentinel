package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/scanner"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
)

// mockSettingsStore implements SettingsStore for testing. It stores settings
// in memory and can optionally return an error on SaveSetting calls.
type mockSettingsStore struct {
	data    map[string]string
	saveErr error // when non-nil, SaveSetting returns this error
}

func newMockSettingsStore() *mockSettingsStore {
	return &mockSettingsStore{data: make(map[string]string)}
}

func (m *mockSettingsStore) SaveSetting(key, value string) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.data[key] = value
	return nil
}

func (m *mockSettingsStore) LoadSetting(key string) (string, error) {
	return m.data[key], nil
}

func (m *mockSettingsStore) GetAllSettings() (map[string]string, error) {
	cp := make(map[string]string, len(m.data))
	for k, v := range m.data {
		cp[k] = v
	}
	return cp, nil
}

// mockClusterLifecycle implements ClusterLifecycle for testing.
type mockClusterLifecycle struct {
	started  bool
	stopped  bool
	startErr error // when non-nil, Start returns this error
}

func (m *mockClusterLifecycle) Start() error {
	if m.startErr != nil {
		return m.startErr
	}
	m.started = true
	return nil
}

func (m *mockClusterLifecycle) Stop() {
	m.stopped = true
}

// newTestServer creates a minimal Server with the given SettingsStore wired in.
// Auth/templates/other deps are left nil — the handlers under test don't need them.
func newTestServer(ss SettingsStore) *Server {
	return &Server{
		deps: Dependencies{
			SettingsStore: ss,
		},
	}
}

func TestApiClusterSettings_ReturnsDefaults(t *testing.T) {
	srv := newTestServer(newMockSettingsStore())

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/settings/cluster", nil)

	srv.apiClusterSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	want := map[string]string{
		"enabled":       "false",
		"port":          "9443",
		"grace_period":  "30m",
		"remote_policy": "manual",
	}
	for k, wantV := range want {
		if got[k] != wantV {
			t.Errorf("key %q = %q, want %q", k, got[k], wantV)
		}
	}
}

func TestApiClusterSettings_ReturnsStoredValues(t *testing.T) {
	ms := newMockSettingsStore()
	ms.data["cluster_enabled"] = "true"
	ms.data["cluster_port"] = "8443"
	ms.data["cluster_grace_period"] = "1h"
	ms.data["cluster_remote_policy"] = "auto"

	srv := newTestServer(ms)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/settings/cluster", nil)
	srv.apiClusterSettings(w, r)

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	want := map[string]string{
		"enabled":       "true",
		"port":          "8443",
		"grace_period":  "1h",
		"remote_policy": "auto",
	}
	for k, wantV := range want {
		if got[k] != wantV {
			t.Errorf("key %q = %q, want %q", k, got[k], wantV)
		}
	}
}

func TestApiClusterSettingsSave_ValidData(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	body := `{"enabled":true,"port":"9443","grace_period":"15m","remote_policy":"auto"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader(body))

	srv.apiClusterSettingsSave(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify all values were persisted.
	if ms.data["cluster_enabled"] != "true" {
		t.Errorf("cluster_enabled = %q, want %q", ms.data["cluster_enabled"], "true")
	}
	if ms.data["cluster_port"] != "9443" {
		t.Errorf("cluster_port = %q, want %q", ms.data["cluster_port"], "9443")
	}
	if ms.data["cluster_grace_period"] != "15m" {
		t.Errorf("cluster_grace_period = %q, want %q", ms.data["cluster_grace_period"], "15m")
	}
	if ms.data["cluster_remote_policy"] != "auto" {
		t.Errorf("cluster_remote_policy = %q, want %q", ms.data["cluster_remote_policy"], "auto")
	}
}

func TestApiClusterSettingsSave_InvalidPort(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	cases := []struct {
		name string
		port string
	}{
		{"too low", "999"},
		{"too high", "70000"},
		{"not a number", "abc"},
		{"negative", "-1"},
		{"zero", "0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"port":%q}`, tc.port)
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader(body))

			srv.apiClusterSettingsSave(w, r)

			if w.Code != http.StatusBadRequest {
				t.Errorf("port=%q: status = %d, want %d", tc.port, w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestApiClusterSettingsSave_InvalidGracePeriod(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	invalidPeriods := []string{"10m", "3h", "24h", "invalid", "1d"}
	for _, gp := range invalidPeriods {
		t.Run(gp, func(t *testing.T) {
			body := fmt.Sprintf(`{"grace_period":%q}`, gp)
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader(body))

			srv.apiClusterSettingsSave(w, r)

			if w.Code != http.StatusBadRequest {
				t.Errorf("grace_period=%q: status = %d, want %d", gp, w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestApiClusterSettingsSave_InvalidPolicy(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	invalidPolicies := []string{"aggressive", "MANUAL", "none", ""}
	for _, pol := range invalidPolicies {
		// Empty string is "no change" and should be accepted (the handler skips empty values).
		if pol == "" {
			continue
		}
		t.Run(pol, func(t *testing.T) {
			body := fmt.Sprintf(`{"remote_policy":%q}`, pol)
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader(body))

			srv.apiClusterSettingsSave(w, r)

			if w.Code != http.StatusBadRequest {
				t.Errorf("remote_policy=%q: status = %d, want %d", pol, w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestApiClusterSettingsSave_SaveError(t *testing.T) {
	ms := newMockSettingsStore()
	ms.saveErr = errors.New("disk full")
	srv := newTestServer(ms)

	body := `{"port":"9443"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader(body))

	srv.apiClusterSettingsSave(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
}

func TestApiClusterSettingsSave_EnableCallsLifecycleStart(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)
	lc := &mockClusterLifecycle{}
	srv.clusterLifecycle = lc

	body := `{"enabled":true}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader(body))

	srv.apiClusterSettingsSave(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if !lc.started {
		t.Error("expected ClusterLifecycle.Start() to be called")
	}
}

func TestApiClusterSettingsSave_DisableCallsLifecycleStop(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)
	lc := &mockClusterLifecycle{}
	srv.clusterLifecycle = lc

	body := `{"enabled":false}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader(body))

	srv.apiClusterSettingsSave(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if !lc.stopped {
		t.Error("expected ClusterLifecycle.Stop() to be called")
	}
}

func TestApiClusterSettingsSave_StartFailureRollsBack(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)
	lc := &mockClusterLifecycle{startErr: errors.New("port in use")}
	srv.clusterLifecycle = lc

	body := `{"enabled":true}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader(body))

	srv.apiClusterSettingsSave(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}

	// Verify the enabled setting was rolled back to "false".
	if ms.data["cluster_enabled"] != "false" {
		t.Errorf("cluster_enabled after rollback = %q, want %q", ms.data["cluster_enabled"], "false")
	}
}

func TestApiClusterSettingsSave_NoSettingsStore(t *testing.T) {
	srv := newTestServer(nil)

	body := `{"port":"9443"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader(body))

	srv.apiClusterSettingsSave(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestApiClusterSettingsSave_InvalidJSON(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader("{invalid"))

	srv.apiClusterSettingsSave(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestApiClusterSettingsSave_PartialUpdate(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	// Only send port — other fields should not be written.
	body := `{"port":"8443"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader(body))

	srv.apiClusterSettingsSave(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if ms.data["cluster_port"] != "8443" {
		t.Errorf("cluster_port = %q, want %q", ms.data["cluster_port"], "8443")
	}

	// Other settings should not have been touched.
	if _, exists := ms.data["cluster_enabled"]; exists {
		t.Error("cluster_enabled should not be set for partial update without 'enabled' field")
	}
	if _, exists := ms.data["cluster_grace_period"]; exists {
		t.Error("cluster_grace_period should not be set")
	}
	if _, exists := ms.data["cluster_remote_policy"]; exists {
		t.Error("cluster_remote_policy should not be set")
	}
}

func TestApiClusterSettingsSave_ValidEdgePorts(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	// Test boundary values: 1024 and 65535 should both be accepted.
	for _, port := range []string{"1024", "65535"} {
		ms.data = make(map[string]string) // reset
		body := fmt.Sprintf(`{"port":%q}`, port)
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader(body))

		srv.apiClusterSettingsSave(w, r)

		if w.Code != http.StatusOK {
			t.Errorf("port=%s: status = %d, want %d", port, w.Code, http.StatusOK)
		}
	}
}

func TestApiClusterSettingsSave_ValidGracePeriods(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	for _, gp := range []string{"5m", "15m", "30m", "1h", "2h"} {
		ms.data = make(map[string]string) // reset
		body := fmt.Sprintf(`{"grace_period":%q}`, gp)
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader(body))

		srv.apiClusterSettingsSave(w, r)

		if w.Code != http.StatusOK {
			t.Errorf("grace_period=%s: status = %d, want %d", gp, w.Code, http.StatusOK)
		}
	}
}

func TestApiClusterSettingsSave_ValidPolicies(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	for _, pol := range []string{"auto", "manual", "pinned"} {
		ms.data = make(map[string]string) // reset
		body := fmt.Sprintf(`{"remote_policy":%q}`, pol)
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/settings/cluster", strings.NewReader(body))

		srv.apiClusterSettingsSave(w, r)

		if w.Code != http.StatusOK {
			t.Errorf("remote_policy=%s: status = %d, want %d", pol, w.Code, http.StatusOK)
		}
	}
}

func TestApiSaveGeneralSetting_SelfUpdateMode(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	// Valid: "manual"
	body := `{"key":"self_update_mode","value":"manual"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/general", strings.NewReader(body))
	srv.apiSaveGeneralSetting(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("manual: status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	if ms.data["self_update_mode"] != "manual" {
		t.Errorf("stored = %q, want %q", ms.data["self_update_mode"], "manual")
	}

	// Valid: "auto"
	body = `{"key":"self_update_mode","value":"auto"}`
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/settings/general", strings.NewReader(body))
	srv.apiSaveGeneralSetting(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("auto: status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	if ms.data["self_update_mode"] != "auto" {
		t.Errorf("stored = %q, want %q", ms.data["self_update_mode"], "auto")
	}

	// Invalid: "bogus"
	body = `{"key":"self_update_mode","value":"bogus"}`
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/settings/general", strings.NewReader(body))
	srv.apiSaveGeneralSetting(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bogus: status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// ---------------------------------------------------------------------------
// apiScannerSettings tests
// ---------------------------------------------------------------------------

func TestApiScannerSettings_Defaults(t *testing.T) {
	srv := newTestServer(newMockSettingsStore())

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/settings/scanner", nil)
	srv.apiScannerSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	want := map[string]string{
		"mode":       string(scanner.ScanDisabled),
		"threshold":  string(scanner.SeverityHigh),
		"trivy_path": "trivy",
	}
	for k, wantV := range want {
		if got[k] != wantV {
			t.Errorf("key %q = %q, want %q", k, got[k], wantV)
		}
	}
}

func TestApiScannerSettings_StoredValues(t *testing.T) {
	ms := newMockSettingsStore()
	ms.data[store.SettingScannerMode] = string(scanner.ScanPreUpdate)
	ms.data[store.SettingScannerThreshold] = string(scanner.SeverityCritical)
	ms.data[store.SettingTrivyPath] = "/usr/local/bin/trivy"

	srv := newTestServer(ms)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/settings/scanner", nil)
	srv.apiScannerSettings(w, r)

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got["mode"] != string(scanner.ScanPreUpdate) {
		t.Errorf("mode = %q, want %q", got["mode"], string(scanner.ScanPreUpdate))
	}
	if got["threshold"] != string(scanner.SeverityCritical) {
		t.Errorf("threshold = %q, want %q", got["threshold"], string(scanner.SeverityCritical))
	}
	if got["trivy_path"] != "/usr/local/bin/trivy" {
		t.Errorf("trivy_path = %q, want %q", got["trivy_path"], "/usr/local/bin/trivy")
	}
}

func TestApiScannerSettings_NilSettingsStore(t *testing.T) {
	srv := newTestServer(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/settings/scanner", nil)
	srv.apiScannerSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Should still return defaults even with nil store.
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["mode"] != string(scanner.ScanDisabled) {
		t.Errorf("mode = %q, want %q", got["mode"], string(scanner.ScanDisabled))
	}
}

// ---------------------------------------------------------------------------
// apiScannerSettingsSave tests
// ---------------------------------------------------------------------------

func TestApiScannerSettingsSave_ValidData(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	body := `{"mode":"pre-update","threshold":"CRITICAL","trivy_path":"/opt/trivy"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/scanner", strings.NewReader(body))
	srv.apiScannerSettingsSave(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if ms.data[store.SettingScannerMode] != "pre-update" {
		t.Errorf("scanner_mode = %q, want %q", ms.data[store.SettingScannerMode], "pre-update")
	}
	if ms.data[store.SettingScannerThreshold] != "CRITICAL" {
		t.Errorf("scanner_threshold = %q, want %q", ms.data[store.SettingScannerThreshold], "CRITICAL")
	}
	if ms.data[store.SettingTrivyPath] != "/opt/trivy" {
		t.Errorf("trivy_path = %q, want %q", ms.data[store.SettingTrivyPath], "/opt/trivy")
	}
}

func TestApiScannerSettingsSave_AllValidModes(t *testing.T) {
	modes := []string{
		string(scanner.ScanDisabled),
		string(scanner.ScanPreUpdate),
		string(scanner.ScanPostUpdate),
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			ms := newMockSettingsStore()
			srv := newTestServer(ms)

			body := fmt.Sprintf(`{"mode":%q}`, mode)
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/settings/scanner", strings.NewReader(body))
			srv.apiScannerSettingsSave(w, r)

			if w.Code != http.StatusOK {
				t.Errorf("mode=%s: status = %d, want %d", mode, w.Code, http.StatusOK)
			}
		})
	}
}

func TestApiScannerSettingsSave_InvalidMode(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	body := `{"mode":"always-scan"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/scanner", strings.NewReader(body))
	srv.apiScannerSettingsSave(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestApiScannerSettingsSave_AllValidThresholds(t *testing.T) {
	thresholds := []scanner.Severity{
		scanner.SeverityCritical,
		scanner.SeverityHigh,
		scanner.SeverityMedium,
		scanner.SeverityLow,
	}
	for _, threshold := range thresholds {
		t.Run(string(threshold), func(t *testing.T) {
			ms := newMockSettingsStore()
			srv := newTestServer(ms)

			body := fmt.Sprintf(`{"threshold":%q}`, string(threshold))
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/settings/scanner", strings.NewReader(body))
			srv.apiScannerSettingsSave(w, r)

			if w.Code != http.StatusOK {
				t.Errorf("threshold=%s: status = %d, want %d", string(threshold), w.Code, http.StatusOK)
			}
		})
	}
}

func TestApiScannerSettingsSave_InvalidThreshold(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	body := `{"threshold":"ULTRA"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/scanner", strings.NewReader(body))
	srv.apiScannerSettingsSave(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestApiScannerSettingsSave_InvalidJSON(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/scanner", strings.NewReader("{broken"))
	srv.apiScannerSettingsSave(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestApiScannerSettingsSave_NilSettingsStore(t *testing.T) {
	srv := newTestServer(nil)

	body := `{"mode":"disabled"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/scanner", strings.NewReader(body))
	srv.apiScannerSettingsSave(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestApiScannerSettingsSave_SaveError(t *testing.T) {
	ms := newMockSettingsStore()
	ms.saveErr = errors.New("disk full")
	srv := newTestServer(ms)

	body := `{"mode":"disabled"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/scanner", strings.NewReader(body))
	srv.apiScannerSettingsSave(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestApiScannerSettingsSave_PartialUpdate(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	// Only send mode, not threshold or trivy_path.
	body := `{"mode":"post-update"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/scanner", strings.NewReader(body))
	srv.apiScannerSettingsSave(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if ms.data[store.SettingScannerMode] != "post-update" {
		t.Errorf("scanner_mode = %q, want %q", ms.data[store.SettingScannerMode], "post-update")
	}
	// Threshold and trivy_path should not be touched.
	if _, exists := ms.data[store.SettingScannerThreshold]; exists {
		t.Error("scanner_threshold should not be set for partial update")
	}
	if _, exists := ms.data[store.SettingTrivyPath]; exists {
		t.Error("trivy_path should not be set for partial update")
	}
}

// ---------------------------------------------------------------------------
// apiVerifierSettings tests
// ---------------------------------------------------------------------------

func TestApiVerifierSettings_Defaults(t *testing.T) {
	srv := newTestServer(newMockSettingsStore())

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/settings/verifier", nil)
	srv.apiVerifierSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	want := map[string]string{
		"mode":        string(verify.ModeDisabled),
		"cosign_path": "cosign",
		"keyless":     "false",
		"key_path":    "",
	}
	for k, wantV := range want {
		if got[k] != wantV {
			t.Errorf("key %q = %q, want %q", k, got[k], wantV)
		}
	}
}

func TestApiVerifierSettings_StoredValues(t *testing.T) {
	ms := newMockSettingsStore()
	ms.data[store.SettingVerifyMode] = string(verify.ModeEnforce)
	ms.data[store.SettingCosignPath] = "/usr/local/bin/cosign"
	ms.data[store.SettingCosignKeyless] = "true"
	ms.data[store.SettingCosignKeyPath] = "/etc/cosign/key.pub"

	srv := newTestServer(ms)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/settings/verifier", nil)
	srv.apiVerifierSettings(w, r)

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got["mode"] != string(verify.ModeEnforce) {
		t.Errorf("mode = %q, want %q", got["mode"], string(verify.ModeEnforce))
	}
	if got["cosign_path"] != "/usr/local/bin/cosign" {
		t.Errorf("cosign_path = %q, want %q", got["cosign_path"], "/usr/local/bin/cosign")
	}
	if got["keyless"] != "true" {
		t.Errorf("keyless = %q, want %q", got["keyless"], "true")
	}
	if got["key_path"] != "/etc/cosign/key.pub" {
		t.Errorf("key_path = %q, want %q", got["key_path"], "/etc/cosign/key.pub")
	}
}

func TestApiVerifierSettings_NilSettingsStore(t *testing.T) {
	srv := newTestServer(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/settings/verifier", nil)
	srv.apiVerifierSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["mode"] != string(verify.ModeDisabled) {
		t.Errorf("mode = %q, want %q", got["mode"], string(verify.ModeDisabled))
	}
}

// ---------------------------------------------------------------------------
// apiVerifierSettingsSave tests
// ---------------------------------------------------------------------------

func TestApiVerifierSettingsSave_ValidData(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	body := `{"mode":"enforce","cosign_path":"/opt/cosign","keyless":true,"key_path":"/keys/pub.pem"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/verifier", strings.NewReader(body))
	srv.apiVerifierSettingsSave(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if ms.data[store.SettingVerifyMode] != "enforce" {
		t.Errorf("verify_mode = %q, want %q", ms.data[store.SettingVerifyMode], "enforce")
	}
	if ms.data[store.SettingCosignPath] != "/opt/cosign" {
		t.Errorf("cosign_path = %q, want %q", ms.data[store.SettingCosignPath], "/opt/cosign")
	}
	if ms.data[store.SettingCosignKeyless] != "true" {
		t.Errorf("cosign_keyless = %q, want %q", ms.data[store.SettingCosignKeyless], "true")
	}
	if ms.data[store.SettingCosignKeyPath] != "/keys/pub.pem" {
		t.Errorf("cosign_key_path = %q, want %q", ms.data[store.SettingCosignKeyPath], "/keys/pub.pem")
	}
}

func TestApiVerifierSettingsSave_AllValidModes(t *testing.T) {
	modes := []string{
		string(verify.ModeDisabled),
		string(verify.ModeWarn),
		string(verify.ModeEnforce),
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			ms := newMockSettingsStore()
			srv := newTestServer(ms)

			body := fmt.Sprintf(`{"mode":%q}`, mode)
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/settings/verifier", strings.NewReader(body))
			srv.apiVerifierSettingsSave(w, r)

			if w.Code != http.StatusOK {
				t.Errorf("mode=%s: status = %d, want %d", mode, w.Code, http.StatusOK)
			}
		})
	}
}

func TestApiVerifierSettingsSave_InvalidMode(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	body := `{"mode":"strict"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/verifier", strings.NewReader(body))
	srv.apiVerifierSettingsSave(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestApiVerifierSettingsSave_InvalidJSON(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/verifier", strings.NewReader("{broken"))
	srv.apiVerifierSettingsSave(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestApiVerifierSettingsSave_NilSettingsStore(t *testing.T) {
	srv := newTestServer(nil)

	body := `{"mode":"warn"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/verifier", strings.NewReader(body))
	srv.apiVerifierSettingsSave(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestApiVerifierSettingsSave_SaveError(t *testing.T) {
	ms := newMockSettingsStore()
	ms.saveErr = errors.New("write failed")
	srv := newTestServer(ms)

	body := `{"mode":"warn"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/verifier", strings.NewReader(body))
	srv.apiVerifierSettingsSave(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestApiVerifierSettingsSave_KeylessFalse(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	body := `{"mode":"warn","keyless":false}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/verifier", strings.NewReader(body))
	srv.apiVerifierSettingsSave(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if ms.data[store.SettingCosignKeyless] != "false" {
		t.Errorf("cosign_keyless = %q, want %q", ms.data[store.SettingCosignKeyless], "false")
	}
}

func TestApiVerifierSettingsSave_KeylessOmitted(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	// When keyless is omitted from the JSON, the *bool pointer is nil
	// and should not be persisted.
	body := `{"mode":"warn"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/verifier", strings.NewReader(body))
	srv.apiVerifierSettingsSave(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if _, exists := ms.data[store.SettingCosignKeyless]; exists {
		t.Error("cosign_keyless should not be set when keyless is omitted from request")
	}
}

func TestApiVerifierSettingsSave_PartialUpdate(t *testing.T) {
	ms := newMockSettingsStore()
	srv := newTestServer(ms)

	// Only send cosign_path.
	body := `{"cosign_path":"/custom/cosign"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/settings/verifier", strings.NewReader(body))
	srv.apiVerifierSettingsSave(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if ms.data[store.SettingCosignPath] != "/custom/cosign" {
		t.Errorf("cosign_path = %q, want %q", ms.data[store.SettingCosignPath], "/custom/cosign")
	}
	// Mode should not be touched.
	if _, exists := ms.data[store.SettingVerifyMode]; exists {
		t.Error("verify_mode should not be set for partial update without 'mode' field")
	}
}

func getSettings(t *testing.T, srv *Server) map[string]string {
	t.Helper()
	w := httptest.NewRecorder()
	srv.apiSettings(w, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// A fresh database has no toggle keys, so the page used to show the toggles
// as Off (or :latest auto-update as On) whatever the environment said.
func TestApiSettings_ReportsEffectiveTogglesOnFreshDB(t *testing.T) {
	ss := newMockSettingsStore()
	ss.data["paused"] = "true"
	cfg := &mockConfigReader{
		values: map[string]string{
			"SENTINEL_HOOKS":              "true",
			"SENTINEL_HOOKS_WRITE_LABELS": "true",
		},
		latestAutoUpdate: false,
		imageCleanup:     true,
		dependencyAware:  true,
		hooksEnabled:     true,
		hooksWriteLabels: true,
	}
	srv := &Server{deps: Dependencies{SettingsStore: ss, Config: cfg, Log: slog.Default()}}

	got := getSettings(t, srv)
	want := map[string]string{
		"latest_auto_update": "false",
		"image_cleanup":      "true",
		"dependency_aware":   "true",
		"hooks_enabled":      "true",
		"hooks_write_labels": "true",
		"paused":             "true", // other saved settings still come through
		"SENTINEL_HOOKS":     "true",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// The runtime config is what the engine uses, so it wins over a stored key.
func TestApiSettings_TogglesFollowRuntimeConfig(t *testing.T) {
	ss := newMockSettingsStore()
	ss.data["hooks_enabled"] = "true"
	ss.data["image_cleanup"] = "true"
	ss.data["latest_auto_update"] = "true"
	cfg := &mockConfigReader{values: map[string]string{}}
	srv := &Server{deps: Dependencies{SettingsStore: ss, Config: cfg, Log: slog.Default()}}

	got := getSettings(t, srv)
	for _, k := range []string{"hooks_enabled", "image_cleanup", "latest_auto_update", "dependency_aware", "hooks_write_labels"} {
		if got[k] != "false" {
			t.Errorf("%s = %q, want \"false\" from the runtime config", k, got[k])
		}
	}
}

// Image backup, remove volumes and the rollback policy are read by the engine
// from the runtime config, so the page must show that value, and say whether
// it came from the environment or from a value saved in the UI.
func TestApiSettings_ReportsEffectiveBackupVolumesRollback(t *testing.T) {
	ss := newMockSettingsStore()
	cfg := &mockConfigReader{
		values:         map[string]string{},
		imageBackup:    true,
		removeVolumes:  true,
		rollbackPolicy: "manual",
	}
	srv := &Server{deps: Dependencies{SettingsStore: ss, Config: cfg, Log: slog.Default()}}

	got := getSettings(t, srv)
	want := map[string]string{
		"image_backup":           "true",
		"remove_volumes":         "true",
		"rollback_policy":        "manual",
		"image_backup_source":    "env",
		"remove_volumes_source":  "env",
		"rollback_policy_source": "env",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	// A value saved in the UI is reported as the source once it exists.
	ss.data["image_backup"] = "false"
	cfg.imageBackup = false
	got = getSettings(t, srv)
	if got["image_backup"] != "false" || got["image_backup_source"] != "saved" {
		t.Errorf("image_backup = %q (%s), want false (saved)", got["image_backup"], got["image_backup_source"])
	}
}

type recordingConfigWriter struct {
	ConfigWriter
	imageBackup *bool
}

func (r *recordingConfigWriter) SetImageBackup(b bool) { r.imageBackup = &b }

func TestApiSetImageBackup_UpdatesRuntimeConfig(t *testing.T) {
	ss := newMockSettingsStore()
	cw := &recordingConfigWriter{}
	srv := &Server{deps: Dependencies{SettingsStore: ss, ConfigWriter: cw, Log: slog.Default()}}
	w := httptest.NewRecorder()
	srv.apiSetImageBackup(w, httptest.NewRequest(http.MethodPost, "/api/settings/image-backup", strings.NewReader(`{"enabled":false}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if cw.imageBackup == nil || *cw.imageBackup {
		t.Errorf("runtime image backup not set to false after the toggle was saved")
	}
}

// The UI rollback and the engine must agree on the policy, so both use the
// runtime config (env default, replaced by a value saved in the UI).
func TestEffectiveRollbackPolicy_UsesRuntimeConfig(t *testing.T) {
	ss := newMockSettingsStore()
	srv := &Server{deps: Dependencies{SettingsStore: ss, Config: &mockConfigReader{rollbackPolicy: "pinned"}}}
	if got := srv.effectiveRollbackPolicy(); got != "pinned" {
		t.Errorf("effectiveRollbackPolicy = %q, want pinned from the env-backed config", got)
	}
	srv = &Server{deps: Dependencies{SettingsStore: ss}}
	ss.data["rollback_policy"] = "manual"
	if got := srv.effectiveRollbackPolicy(); got != "manual" {
		t.Errorf("effectiveRollbackPolicy without config = %q, want the saved manual", got)
	}
}

// The page warns when verification is on but cosign cannot be found, so the
// field reports availability for the configured path.
func TestApiVerifierSettings_ReportsCosignAvailability(t *testing.T) {
	ss := newMockSettingsStore()
	ss.data[store.SettingVerifyMode] = "enforce"
	ss.data[store.SettingCosignPath] = "/nonexistent/cosign-binary"
	srv := newTestServer(ss)
	w := httptest.NewRecorder()
	srv.apiVerifierSettings(w, httptest.NewRequest(http.MethodGet, "/api/settings/verifier", nil))
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["cosign_available"] != "false" {
		t.Errorf("cosign_available = %q, want false for a missing binary", got["cosign_available"])
	}

	ss.data[store.SettingCosignPath] = "sh" // any binary on PATH
	w = httptest.NewRecorder()
	srv.apiVerifierSettings(w, httptest.NewRequest(http.MethodGet, "/api/settings/verifier", nil))
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["cosign_available"] != "true" {
		t.Errorf("cosign_available = %q, want true when the binary is found", got["cosign_available"])
	}
}

func TestApiSettings_ReportsHistoryRetention(t *testing.T) {
	ss := newMockSettingsStore()
	cfg := &mockConfigReader{values: map[string]string{"SENTINEL_HISTORY_RETENTION_DAYS": "30"}}
	srv := &Server{deps: Dependencies{SettingsStore: ss, Config: cfg, Log: slog.Default()}}

	got := getSettings(t, srv)
	if got["history_retention_days"] != "30" || got["history_retention_days_source"] != "env" {
		t.Errorf("from env: %q (%s), want 30 (env)", got["history_retention_days"], got["history_retention_days_source"])
	}

	ss.data["history_retention_days"] = "0"
	got = getSettings(t, srv)
	if got["history_retention_days"] != "0" || got["history_retention_days_source"] != "saved" {
		t.Errorf("saved 0: %q (%s), want 0 (saved)", got["history_retention_days"], got["history_retention_days_source"])
	}

	srv = &Server{deps: Dependencies{SettingsStore: newMockSettingsStore(), Config: &mockConfigReader{values: map[string]string{}}, Log: slog.Default()}}
	if got := getSettings(t, srv); got["history_retention_days"] != "90" {
		t.Errorf("default = %q, want 90", got["history_retention_days"])
	}
}

func TestApiSetHistoryRetention(t *testing.T) {
	for _, tc := range []struct {
		body string
		code int
		want string
	}{
		{`{"days":30}`, http.StatusOK, "30"},
		{`{"days":0}`, http.StatusOK, "0"},
		{`{"days":-1}`, http.StatusBadRequest, ""},
		{`{"days":100000}`, http.StatusBadRequest, ""},
		{`not json`, http.StatusBadRequest, ""},
	} {
		ss := newMockSettingsStore()
		srv := newTestServer(ss)
		w := httptest.NewRecorder()
		srv.apiSetHistoryRetention(w, httptest.NewRequest(http.MethodPost, "/api/settings/history-retention", strings.NewReader(tc.body)))
		if w.Code != tc.code {
			t.Errorf("%s: status = %d, want %d", tc.body, w.Code, tc.code)
		}
		if got := ss.data["history_retention_days"]; got != tc.want {
			t.Errorf("%s: saved %q, want %q", tc.body, got, tc.want)
		}
	}
}

// Settings set only by the environment must reach the page: it shows them as
// the current values, so nothing is written back unchanged.
func TestApiSettings_EnvOnlyValuesAreReported(t *testing.T) {
	ss := newMockSettingsStore()
	cfg := &mockConfigReader{values: map[string]string{
		"SENTINEL_POLL_INTERVAL":      "10m0s",
		"SENTINEL_GRACE_PERIOD":       "30s",
		"SENTINEL_DEFAULT_POLICY":     "auto",
		"SENTINEL_SCHEDULE":           "0 3 * * *",
		"SENTINEL_SHOW_STOPPED":       "true",
		"SENTINEL_SCAN_CONCURRENCY":   "4",
		"SENTINEL_MAINTENANCE_WINDOW": "02:00-04:00",
	}}
	srv := &Server{deps: Dependencies{SettingsStore: ss, Config: cfg, Log: slog.Default()}}
	got := getSettings(t, srv)
	want := map[string]string{
		"poll_interval": "10m0s", "grace_period": "30s", "default_policy": "auto", "schedule": "0 3 * * *",
		"show_stopped": "true", "scan_concurrency": "4", "maintenance_window": "02:00-04:00",
	}
	for k, v := range want {
		if got[k] != v || got[k+"_source"] != "env" {
			t.Errorf("%s = %q (%s), want %q (env)", k, got[k], got[k+"_source"], v)
		}
	}
	// A saved value wins and says so.
	ss.data["poll_interval"] = "30m"
	got = getSettings(t, srv)
	if got["poll_interval"] != "30m" || got["poll_interval_source"] != "saved" {
		t.Errorf("saved poll interval = %q (%s)", got["poll_interval"], got["poll_interval_source"])
	}
}
