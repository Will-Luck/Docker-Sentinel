package web

import (
	"encoding/json"
	"errors"
	"fmt"
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
