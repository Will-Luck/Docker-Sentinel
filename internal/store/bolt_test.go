package store

import (
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSnapshotRoundTrip(t *testing.T) {
	s := testStore(t)

	data := []byte(`{"name":"nginx","image":"nginx:1.25"}`)
	if err := s.SaveSnapshot("nginx", data); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	got, err := s.GetLatestSnapshot("nginx")
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

func TestSnapshotLatestWins(t *testing.T) {
	s := testStore(t)

	if err := s.SaveSnapshot("app", []byte("first")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond) // ensure different timestamp
	if err := s.SaveSnapshot("app", []byte("second")); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetLatestSnapshot("app")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Errorf("got %q, want %q", got, "second")
	}
}

func TestSnapshotMissing(t *testing.T) {
	s := testStore(t)

	got, err := s.GetLatestSnapshot("nonexistent")
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %q", got)
	}
}

func TestSnapshotIsolation(t *testing.T) {
	s := testStore(t)

	if err := s.SaveSnapshot("app-a", []byte("data-a")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSnapshot("app-b", []byte("data-b")); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetLatestSnapshot("app-a")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data-a" {
		t.Errorf("app-a snapshot = %q, want %q", got, "data-a")
	}

	got, err = s.GetLatestSnapshot("app-b")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data-b" {
		t.Errorf("app-b snapshot = %q, want %q", got, "data-b")
	}
}

func TestUpdateHistory(t *testing.T) {
	s := testStore(t)

	now := time.Now().UTC()
	records := []UpdateRecord{
		{Timestamp: now.Add(-2 * time.Minute), ContainerName: "nginx", Outcome: "success"},
		{Timestamp: now.Add(-1 * time.Minute), ContainerName: "redis", Outcome: "rollback", Error: "unhealthy after update"},
		{Timestamp: now, ContainerName: "postgres", Outcome: "success"},
	}

	for _, r := range records {
		if err := s.RecordUpdate(r); err != nil {
			t.Fatalf("RecordUpdate: %v", err)
		}
	}

	// List all — should be newest-first.
	got, err := s.ListHistory(10)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d records, want 3", len(got))
	}
	if got[0].ContainerName != "postgres" {
		t.Errorf("first record = %q, want postgres", got[0].ContainerName)
	}
	if got[2].ContainerName != "nginx" {
		t.Errorf("last record = %q, want nginx", got[2].ContainerName)
	}

	// List with limit.
	got, err = s.ListHistory(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].ContainerName != "postgres" {
		t.Errorf("limited record = %q, want postgres", got[0].ContainerName)
	}
}

func TestMaintenance(t *testing.T) {
	s := testStore(t)

	active, err := s.GetMaintenance("app")
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Error("expected false for unset maintenance")
	}

	if err := s.SetMaintenance("app", true); err != nil {
		t.Fatal(err)
	}
	active, err = s.GetMaintenance("app")
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Error("expected true after SetMaintenance(true)")
	}

	if err := s.SetMaintenance("app", false); err != nil {
		t.Fatal(err)
	}
	active, err = s.GetMaintenance("app")
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Error("expected false after SetMaintenance(false)")
	}
}

func TestPendingQueue(t *testing.T) {
	s := testStore(t)

	// Empty initially.
	data, err := s.LoadPendingQueue()
	if err != nil {
		t.Fatal(err)
	}
	if data != nil {
		t.Errorf("expected nil, got %q", data)
	}

	// Save and load.
	queue := []byte(`[{"name":"nginx","id":"abc123"}]`)
	if err := s.SavePendingQueue(queue); err != nil {
		t.Fatal(err)
	}
	data, err = s.LoadPendingQueue()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(queue) {
		t.Errorf("got %q, want %q", data, queue)
	}
}
