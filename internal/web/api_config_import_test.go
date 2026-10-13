package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// An export written by v2.16.x can still carry Portainer settings. Importing it
// must succeed, keep the settings that still exist and never store the rest.
func TestApiConfigImport_IgnoresRemovedPortainerFields(t *testing.T) {
	ms := newMockSettingsStore()
	srv := &Server{deps: Dependencies{SettingsStore: ms, Log: slog.Default()}}

	body := `{
		"version": "1",
		"exported_at": "2026-09-01T00:00:00Z",
		"settings": {
			"default_policy": "manual",
			"portainer_enabled": "true",
			"portainer_url": "https://portainer.example",
			"portainer_token": "[REDACTED]",
			"SENTINEL_PORTAINER_URL": "https://portainer.example"
		},
		"portainer_instances": [{"id": "p1", "name": "edge", "url": "https://portainer.example", "token": "abc"}]
	}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/config/import", strings.NewReader(body))

	srv.apiConfigImport(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var res ConfigImportResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Settings != 1 {
		t.Errorf("settings imported = %d, want 1", res.Settings)
	}
	if ms.data["default_policy"] != "manual" {
		t.Errorf("default_policy = %q, want manual", ms.data["default_policy"])
	}
	for k := range ms.data {
		if strings.Contains(strings.ToLower(k), "portainer") {
			t.Errorf("portainer key %q was stored", k)
		}
	}
}

// History rows written by the removed connector keep a "portainer:" host ID.
// The history page must still render them without any connector lookup.
func TestHandleHistory_RendersRemovedConnectorRows(t *testing.T) {
	srv := &Server{deps: Dependencies{
		Store: &feedHistoryStore{records: []UpdateRecord{{
			Timestamp:     time.Now(),
			ContainerName: "web",
			HostID:        "portainer:p1:3",
			HostName:      "edge",
			Outcome:       "success",
		}}},
		Queue: &mockUpdateQueue{},
		Log:   slog.Default(),
	}}
	srv.parseTemplates()

	w := httptest.NewRecorder()
	srv.handleHistory(w, httptest.NewRequest(http.MethodGet, "/history", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "edge") {
		t.Error("host name missing from rendered history")
	}
}
