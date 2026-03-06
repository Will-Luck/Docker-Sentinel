package backup

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

type testLogger struct {
	infos  []string
	errors []string
}

func (l *testLogger) Info(msg string, args ...any)  { l.infos = append(l.infos, msg) }
func (l *testLogger) Error(msg string, args ...any) { l.errors = append(l.errors, msg) }

type mockUploader struct {
	uploaded []string
	err      error
}

func (m *mockUploader) Upload(_ context.Context, localPath, objectName string) error {
	if m.err != nil {
		return m.err
	}
	m.uploaded = append(m.uploaded, objectName)
	return nil
}

func newTestDB(t *testing.T) *bolt.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		t.Fatalf("open bolt: %v", err)
	}
	// Write some test data.
	_ = db.Update(func(tx *bolt.Tx) error {
		b, _ := tx.CreateBucketIfNotExists([]byte("test"))
		return b.Put([]byte("key"), []byte("value"))
	})
	t.Cleanup(func() { db.Close() })
	return db
}

func TestCreateBackup(t *testing.T) {
	db := newTestDB(t)
	dir := t.TempDir()
	log := &testLogger{}

	mgr, err := NewManager(db, dir, log)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	info, err := mgr.CreateBackup(context.Background())
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if info.Size == 0 {
		t.Error("backup size is 0")
	}
	if info.Filename == "" {
		t.Error("backup filename is empty")
	}

	// Verify the backup is a valid BoltDB file.
	backupPath := filepath.Join(dir, info.Filename)
	backupDB, err := bolt.Open(backupPath, 0o600, &bolt.Options{Timeout: 1 * time.Second, ReadOnly: true})
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backupDB.Close()

	var val []byte
	_ = backupDB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("test"))
		if b == nil {
			t.Error("test bucket not found in backup")
			return nil
		}
		val = b.Get([]byte("key"))
		return nil
	})
	if string(val) != "value" {
		t.Errorf("got %q, want %q", val, "value")
	}
}

func TestListBackups(t *testing.T) {
	db := newTestDB(t)
	dir := t.TempDir()
	log := &testLogger{}

	mgr, err := NewManager(db, dir, log)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Create multiple backups.
	for i := 0; i < 3; i++ {
		if _, err := mgr.CreateBackup(context.Background()); err != nil {
			t.Fatalf("CreateBackup %d: %v", i, err)
		}
		time.Sleep(10 * time.Millisecond) // ensure different timestamps
	}

	list, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d backups, want 3", len(list))
	}
	// Should be newest first.
	if list[0].CreatedAt.Before(list[1].CreatedAt) {
		t.Error("backups not sorted newest first")
	}
}

func TestRetention(t *testing.T) {
	db := newTestDB(t)
	dir := t.TempDir()
	log := &testLogger{}

	mgr, err := NewManager(db, dir, log)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.SetRetention(2)

	// Create 4 backups — only 2 should remain.
	for i := 0; i < 4; i++ {
		if _, err := mgr.CreateBackup(context.Background()); err != nil {
			t.Fatalf("CreateBackup %d: %v", i, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	list, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d backups, want 2 (retention limit)", len(list))
	}
}

func TestS3Upload(t *testing.T) {
	db := newTestDB(t)
	dir := t.TempDir()
	log := &testLogger{}
	uploader := &mockUploader{}

	mgr, err := NewManager(db, dir, log)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.SetUploader(uploader)

	info, err := mgr.CreateBackup(context.Background())
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	if len(uploader.uploaded) != 1 {
		t.Fatalf("got %d uploads, want 1", len(uploader.uploaded))
	}
	if uploader.uploaded[0] != info.Filename {
		t.Errorf("uploaded %q, want %q", uploader.uploaded[0], info.Filename)
	}
}

func TestS3UploadFailureDoesNotFailBackup(t *testing.T) {
	db := newTestDB(t)
	dir := t.TempDir()
	log := &testLogger{}
	uploader := &mockUploader{err: fmt.Errorf("network timeout")}

	mgr, err := NewManager(db, dir, log)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.SetUploader(uploader)

	// Backup should still succeed even if S3 upload fails.
	info, err := mgr.CreateBackup(context.Background())
	if err != nil {
		t.Fatalf("CreateBackup should succeed: %v", err)
	}
	if info.Filename == "" {
		t.Error("no filename returned")
	}

	// Verify the error was logged.
	found := false
	for _, e := range log.errors {
		if e == "S3 upload failed" {
			found = true
		}
	}
	if !found {
		t.Error("expected S3 upload error to be logged")
	}
}

func TestFilePathSanitisation(t *testing.T) {
	db := newTestDB(t)
	dir := t.TempDir()
	log := &testLogger{}

	mgr, err := NewManager(db, dir, log)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Path traversal attempts should fail.
	_, err = mgr.FilePath("../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}

	_, err = mgr.FilePath("foo/bar.db")
	if err == nil {
		t.Error("expected error for path with directory")
	}

	// Non-existent file should fail.
	_, err = mgr.FilePath("nonexistent.db")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}
