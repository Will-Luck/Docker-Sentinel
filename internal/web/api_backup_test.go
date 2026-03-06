package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Mock: BackupManager
// ---------------------------------------------------------------------------

type mockBackupManager struct {
	createInfo *BackupInfo
	createErr  error
	listInfos  []BackupInfo
	listErr    error
	filePaths  map[string]string // filename -> path
	fileErr    error
}

func (m *mockBackupManager) CreateBackup(_ context.Context) (*BackupInfo, error) {
	return m.createInfo, m.createErr
}

func (m *mockBackupManager) List() ([]BackupInfo, error) {
	return m.listInfos, m.listErr
}

func (m *mockBackupManager) FilePath(filename string) (string, error) {
	if m.fileErr != nil {
		return "", m.fileErr
	}
	p, ok := m.filePaths[filename]
	if !ok {
		return "", errors.New("not found")
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

func newBackupTestServer(backup BackupManager) *Server {
	return &Server{
		deps: Dependencies{
			Backup: backup,
			Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
}

// ---------------------------------------------------------------------------
// apiBackupTrigger tests
// ---------------------------------------------------------------------------

func TestApiBackupTrigger_NotConfigured(t *testing.T) {
	srv := newBackupTestServer(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/backup/trigger", nil)
	srv.apiBackupTrigger(w, r)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusNotImplemented, w.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["error"] != "backup not configured" {
		t.Errorf("error = %q, want %q", resp["error"], "backup not configured")
	}
}

func TestApiBackupTrigger_Success(t *testing.T) {
	info := &BackupInfo{
		Filename:  "sentinel-20260306-120000.db",
		Size:      4096,
		CreatedAt: time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC),
	}
	mock := &mockBackupManager{createInfo: info}
	srv := newBackupTestServer(mock)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/backup/trigger", nil)
	srv.apiBackupTrigger(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var got BackupInfo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Filename != info.Filename {
		t.Errorf("Filename = %q, want %q", got.Filename, info.Filename)
	}
	if got.Size != info.Size {
		t.Errorf("Size = %d, want %d", got.Size, info.Size)
	}
}

func TestApiBackupTrigger_CreateError(t *testing.T) {
	mock := &mockBackupManager{createErr: errors.New("disk full")}
	srv := newBackupTestServer(mock)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/backup/trigger", nil)
	srv.apiBackupTrigger(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// apiBackupList tests
// ---------------------------------------------------------------------------

func TestApiBackupList_NotConfigured(t *testing.T) {
	srv := newBackupTestServer(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/backup/list", nil)
	srv.apiBackupList(w, r)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusNotImplemented, w.Body.String())
	}
}

func TestApiBackupList_Success(t *testing.T) {
	infos := []BackupInfo{
		{Filename: "backup-1.db", Size: 1024, CreatedAt: time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)},
		{Filename: "backup-2.db", Size: 2048, CreatedAt: time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)},
	}
	mock := &mockBackupManager{listInfos: infos}
	srv := newBackupTestServer(mock)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/backup/list", nil)
	srv.apiBackupList(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var got []BackupInfo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if got[0].Filename != "backup-1.db" {
		t.Errorf("got[0].Filename = %q, want %q", got[0].Filename, "backup-1.db")
	}
	if got[1].Size != 2048 {
		t.Errorf("got[1].Size = %d, want %d", got[1].Size, 2048)
	}
}

func TestApiBackupList_Error(t *testing.T) {
	mock := &mockBackupManager{listErr: errors.New("read error")}
	srv := newBackupTestServer(mock)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/backup/list", nil)
	srv.apiBackupList(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// apiBackupDownload tests
// ---------------------------------------------------------------------------

func TestApiBackupDownload_NotConfigured(t *testing.T) {
	srv := newBackupTestServer(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/backup/download/test.db", nil)
	r.SetPathValue("filename", "test.db")
	srv.apiBackupDownload(w, r)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusNotImplemented, w.Body.String())
	}
}

func TestApiBackupDownload_MissingFilename(t *testing.T) {
	mock := &mockBackupManager{}
	srv := newBackupTestServer(mock)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/backup/download/", nil)
	// PathValue("filename") returns "" when no filename segment is present.
	srv.apiBackupDownload(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestApiBackupDownload_FileNotFound(t *testing.T) {
	mock := &mockBackupManager{
		filePaths: map[string]string{}, // empty: no files
	}
	srv := newBackupTestServer(mock)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/backup/download/nonexistent.db", nil)
	r.SetPathValue("filename", "nonexistent.db")
	srv.apiBackupDownload(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}
