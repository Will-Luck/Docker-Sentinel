package store

import (
	"fmt"
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
	got, err := s.ListHistory(10, "")
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
	got, err = s.ListHistory(1, "")
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

func TestListSnapshots(t *testing.T) {
	s := testStore(t)

	// Save 3 snapshots for the same container with slight delays.
	if err := s.SaveSnapshot("web", []byte("snap-1")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := s.SaveSnapshot("web", []byte("snap-2")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := s.SaveSnapshot("web", []byte("snap-3")); err != nil {
		t.Fatal(err)
	}

	entries, err := s.ListSnapshots("web")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}

	// Newest first.
	if string(entries[0].Data) != "snap-3" {
		t.Errorf("first entry data = %q, want %q", entries[0].Data, "snap-3")
	}
	if string(entries[2].Data) != "snap-1" {
		t.Errorf("last entry data = %q, want %q", entries[2].Data, "snap-1")
	}

	// Verify timestamps are descending.
	for i := 1; i < len(entries); i++ {
		if entries[i].Timestamp.After(entries[i-1].Timestamp) {
			t.Errorf("entry %d timestamp (%v) is after entry %d (%v)", i, entries[i].Timestamp, i-1, entries[i-1].Timestamp)
		}
	}
}

func TestListSnapshotsEmpty(t *testing.T) {
	s := testStore(t)

	entries, err := s.ListSnapshots("nonexistent")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}

func TestListHistoryByContainer(t *testing.T) {
	s := testStore(t)

	now := time.Now().UTC()
	records := []UpdateRecord{
		{Timestamp: now.Add(-3 * time.Minute), ContainerName: "nginx", Outcome: "success", OldImage: "nginx:1.24"},
		{Timestamp: now.Add(-2 * time.Minute), ContainerName: "redis", Outcome: "success", OldImage: "redis:7"},
		{Timestamp: now.Add(-1 * time.Minute), ContainerName: "nginx", Outcome: "rollback", OldImage: "nginx:1.25"},
		{Timestamp: now, ContainerName: "postgres", Outcome: "success", OldImage: "postgres:16"},
	}

	for _, r := range records {
		if err := s.RecordUpdate(r); err != nil {
			t.Fatalf("RecordUpdate: %v", err)
		}
	}

	// Filter by nginx — should get 2 records, newest first.
	got, err := s.ListHistoryByContainer("nginx", 10)
	if err != nil {
		t.Fatalf("ListHistoryByContainer: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	if got[0].Outcome != "rollback" {
		t.Errorf("first nginx record outcome = %q, want %q", got[0].Outcome, "rollback")
	}
	if got[1].Outcome != "success" {
		t.Errorf("second nginx record outcome = %q, want %q", got[1].Outcome, "success")
	}

	// Filter by redis — should get 1 record.
	got, err = s.ListHistoryByContainer("redis", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d redis records, want 1", len(got))
	}

	// Limit — only 1 nginx record.
	got, err = s.ListHistoryByContainer("nginx", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records with limit 1, want 1", len(got))
	}
	if got[0].Outcome != "rollback" {
		t.Errorf("limited record outcome = %q, want %q", got[0].Outcome, "rollback")
	}
}

func TestDeleteOldSnapshots(t *testing.T) {
	s := testStore(t)

	// Save 5 snapshots.
	for i := 0; i < 5; i++ {
		if err := s.SaveSnapshot("app", []byte(fmt.Sprintf("snap-%d", i))); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond) // ensure different timestamps
	}

	// Delete keeping 2.
	if err := s.DeleteOldSnapshots("app", 2); err != nil {
		t.Fatalf("DeleteOldSnapshots: %v", err)
	}

	entries, err := s.ListSnapshots("app")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	// The 2 newest should remain (snap-4 and snap-3).
	if string(entries[0].Data) != "snap-4" {
		t.Errorf("newest entry = %q, want %q", entries[0].Data, "snap-4")
	}
	if string(entries[1].Data) != "snap-3" {
		t.Errorf("second entry = %q, want %q", entries[1].Data, "snap-3")
	}
}

func TestDeleteOldSnapshotsKeepAll(t *testing.T) {
	s := testStore(t)

	// Save 3 snapshots.
	for i := 0; i < 3; i++ {
		if err := s.SaveSnapshot("svc", []byte(fmt.Sprintf("snap-%d", i))); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}

	// Keep >= count should not delete anything.
	if err := s.DeleteOldSnapshots("svc", 5); err != nil {
		t.Fatalf("DeleteOldSnapshots: %v", err)
	}

	entries, err := s.ListSnapshots("svc")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
}

func TestDigestEquivalenceCacheAndCheck(t *testing.T) {
	s := testStore(t)

	local := "docker.io/library/nginx@sha256:aaa111"
	remote := "sha256:bbb222"

	// Not cached initially.
	if s.CheckDigestEquivalence(local, remote) {
		t.Error("expected false before caching")
	}

	// Cache it.
	if err := s.CacheDigestEquivalence(local, remote); err != nil {
		t.Fatalf("CacheDigestEquivalence: %v", err)
	}

	// Now should be found.
	if !s.CheckDigestEquivalence(local, remote) {
		t.Error("expected true after caching")
	}

	// Different pair should not match.
	if s.CheckDigestEquivalence(local, "sha256:ccc333") {
		t.Error("expected false for different remote digest")
	}
}

func TestDigestEquivalenceClear(t *testing.T) {
	s := testStore(t)

	local := "docker.io/library/nginx@sha256:aaa111"
	remote := "sha256:bbb222"

	if err := s.CacheDigestEquivalence(local, remote); err != nil {
		t.Fatalf("CacheDigestEquivalence: %v", err)
	}

	// Verify cached.
	if !s.CheckDigestEquivalence(local, remote) {
		t.Fatal("expected cached entry to exist")
	}

	// Clear by image ref containing the local hash.
	if err := s.ClearDigestEquivalence("docker.io/library/nginx@sha256:aaa111"); err != nil {
		t.Fatalf("ClearDigestEquivalence: %v", err)
	}

	// Should be gone.
	if s.CheckDigestEquivalence(local, remote) {
		t.Error("expected false after clearing")
	}
}

func TestDigestEquivalenceClearNoHash(t *testing.T) {
	s := testStore(t)

	// ClearDigestEquivalence with no sha256: prefix should be a no-op.
	if err := s.ClearDigestEquivalence("nginx:latest"); err != nil {
		t.Fatalf("ClearDigestEquivalence: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Edge Cases: Duplicate Timestamps
// ---------------------------------------------------------------------------

func TestRecordUpdateDuplicateTimestamp(t *testing.T) {
	s := testStore(t)

	// Two records with the exact same timestamp. BoltDB uses the timestamp as
	// the key, so the second Put overwrites the first. This test documents that
	// behaviour explicitly.
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	r1 := UpdateRecord{Timestamp: ts, ContainerName: "nginx", Outcome: "success"}
	r2 := UpdateRecord{Timestamp: ts, ContainerName: "redis", Outcome: "rollback"}

	if err := s.RecordUpdate(r1); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUpdate(r2); err != nil {
		t.Fatal(err)
	}

	// Because the key is RFC3339Nano of the same timestamp, the second
	// record overwrites the first. Only one should remain.
	got, err := s.ListHistory(10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 record (overwrite), got %d", len(got))
	}
	// The surviving record should be the last one written.
	if got[0].ContainerName != "redis" {
		t.Errorf("surviving record = %q, want redis (last write wins)", got[0].ContainerName)
	}
}

func TestAppendLogDuplicateTimestamp(t *testing.T) {
	s := testStore(t)

	// Same timestamp for two log entries — same overwrite behaviour as history.
	ts := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	e1 := LogEntry{Timestamp: ts, Type: "update", Message: "first"}
	e2 := LogEntry{Timestamp: ts, Type: "policy_set", Message: "second"}

	if err := s.AppendLog(e1); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLog(e2); err != nil {
		t.Fatal(err)
	}

	logs, err := s.ListLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log (overwrite), got %d", len(logs))
	}
	if logs[0].Message != "second" {
		t.Errorf("surviving log = %q, want %q", logs[0].Message, "second")
	}
}

// ---------------------------------------------------------------------------
// Edge Cases: Pagination with before cursor
// ---------------------------------------------------------------------------

func TestListHistoryPagination(t *testing.T) {
	s := testStore(t)

	// Insert 5 records with 1-minute spacing.
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		rec := UpdateRecord{
			Timestamp:     base.Add(time.Duration(i) * time.Minute),
			ContainerName: fmt.Sprintf("app-%d", i),
			Outcome:       "success",
		}
		if err := s.RecordUpdate(rec); err != nil {
			t.Fatal(err)
		}
	}

	// Page 1: newest 2 records.
	page1, err := s.ListHistory(2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1: expected 2 records, got %d", len(page1))
	}
	if page1[0].ContainerName != "app-4" {
		t.Errorf("page1[0] = %q, want app-4", page1[0].ContainerName)
	}
	if page1[1].ContainerName != "app-3" {
		t.Errorf("page1[1] = %q, want app-3", page1[1].ContainerName)
	}

	// Page 2: use the timestamp of the last record on page 1 as cursor.
	cursor := page1[1].Timestamp.UTC().Format(time.RFC3339Nano)
	page2, err := s.ListHistory(2, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 {
		t.Fatalf("page2: expected 2 records, got %d", len(page2))
	}
	if page2[0].ContainerName != "app-2" {
		t.Errorf("page2[0] = %q, want app-2", page2[0].ContainerName)
	}
	if page2[1].ContainerName != "app-1" {
		t.Errorf("page2[1] = %q, want app-1", page2[1].ContainerName)
	}

	// Page 3: should have 1 remaining record.
	cursor = page2[1].Timestamp.UTC().Format(time.RFC3339Nano)
	page3, err := s.ListHistory(2, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(page3) != 1 {
		t.Fatalf("page3: expected 1 record, got %d", len(page3))
	}
	if page3[0].ContainerName != "app-0" {
		t.Errorf("page3[0] = %q, want app-0", page3[0].ContainerName)
	}

	// Page 4: past the end — should be empty.
	cursor = page3[0].Timestamp.UTC().Format(time.RFC3339Nano)
	page4, err := s.ListHistory(2, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(page4) != 0 {
		t.Errorf("page4: expected 0 records, got %d", len(page4))
	}
}
