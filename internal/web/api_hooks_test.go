package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockHookStore implements HookStore in memory.
type mockHookStore struct {
	saved []HookEntry
}

func (m *mockHookStore) ListHooks(string) ([]HookEntry, error) { return m.saved, nil }
func (m *mockHookStore) SaveHook(h HookEntry) error {
	m.saved = append(m.saved, h)
	return nil
}
func (m *mockHookStore) DeleteHook(string, string) error { return nil }

func postHook(t *testing.T, body string) (*httptest.ResponseRecorder, *mockHookStore) {
	t.Helper()
	hs := &mockHookStore{}
	srv := &Server{deps: Dependencies{HookStore: hs, Log: slog.Default()}}
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/postgres", strings.NewReader(body))
	req.SetPathValue("container", "postgres")
	w := httptest.NewRecorder()
	srv.apiSaveHook(w, req)
	return w, hs
}

func TestApiSaveHook_StringCommandRunsInShell(t *testing.T) {
	w, hs := postHook(t, `{"phase":"pre-update","command":"pg_dump -U postgres app > /backups/dump.sql","timeout":120}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if len(hs.saved) != 1 {
		t.Fatalf("saved %d hooks, want 1", len(hs.saved))
	}
	want := []string{"/bin/sh", "-c", "pg_dump -U postgres app > /backups/dump.sql"}
	if got := hs.saved[0].Command; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("command = %q, want %q", got, want)
	}
	if hs.saved[0].Timeout != 120 {
		t.Errorf("timeout = %d, want 120", hs.saved[0].Timeout)
	}
}

func TestApiSaveHook_ArrayCommandKeptAsArgv(t *testing.T) {
	w, hs := postHook(t, `{"phase":"post-update","command":["/usr/bin/curl","-sf","http://localhost/health"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	want := []string{"/usr/bin/curl", "-sf", "http://localhost/health"}
	if got := hs.saved[0].Command; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("command = %q, want %q", got, want)
	}
	if hs.saved[0].Timeout != 30 {
		t.Errorf("timeout = %d, want default 30", hs.saved[0].Timeout)
	}
}

func TestApiSaveHook_RejectsEmptyCommand(t *testing.T) {
	for name, body := range map[string]string{
		"empty string":       `{"phase":"pre-update","command":""}`,
		"blank string":       `{"phase":"pre-update","command":"   "}`,
		"empty array":        `{"phase":"pre-update","command":[]}`,
		"blank program":      `{"phase":"pre-update","command":[""]}`,
		"missing":            `{"phase":"pre-update"}`,
		"null":               `{"phase":"pre-update","command":null}`,
		"number":             `{"phase":"pre-update","command":42}`,
		"array of non-items": `{"phase":"pre-update","command":[1,2]}`,
	} {
		t.Run(name, func(t *testing.T) {
			w, hs := postHook(t, body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
			}
			if len(hs.saved) != 0 {
				t.Errorf("saved %v, want nothing", hs.saved)
			}
		})
	}
}
