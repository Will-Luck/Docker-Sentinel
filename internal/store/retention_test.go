package store

import (
	"testing"
	"time"
)

func TestPruneHistoryKeepsAppliedUpdates(t *testing.T) {
	s := testStore(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -200)
	recent := now.AddDate(0, 0, -5)
	for i, rec := range []UpdateRecord{
		{Timestamp: old, ContainerName: "", Outcome: "scan_summary"},
		{Timestamp: old.Add(time.Second), ContainerName: "web", Outcome: "success"},
		{Timestamp: old.Add(2 * time.Second), ContainerName: "web", Outcome: "failed"},
		{Timestamp: recent, ContainerName: "", Outcome: "scan_summary"},
	} {
		if err := s.RecordUpdate(rec); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	n, err := s.PruneHistory(now.AddDate(0, 0, -90))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned %d, want 2 (old scan summary and old failure)", n)
	}
	left, _ := s.ListAllHistory()
	if len(left) != 2 {
		t.Fatalf("%d records left, want 2", len(left))
	}
	var sawSuccess bool
	for _, r := range left {
		if r.Outcome == "success" {
			sawSuccess = true
		}
	}
	if !sawSuccess {
		t.Error("an applied update was pruned")
	}
}

func TestPruneLogsRemovesOldEntries(t *testing.T) {
	s := testStore(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	_ = s.AppendLog(LogEntry{Timestamp: now.AddDate(0, 0, -100), Type: "update", Message: "old"})
	_ = s.AppendLog(LogEntry{Timestamp: now.AddDate(0, 0, -1), Type: "update", Message: "new"})
	n, err := s.PruneLogs(now.AddDate(0, 0, -90))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, err %v; want 1", n, err)
	}
	left, _ := s.ListLogs(10)
	if len(left) != 1 || left[0].Message != "new" {
		t.Fatalf("unexpected logs left: %+v", left)
	}
}

func TestPruneHistoryKeepsPartialUpdates(t *testing.T) {
	s := testStore(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -200)
	_ = s.RecordUpdate(UpdateRecord{Timestamp: old, ContainerName: "web", OldImage: "web:1", Outcome: "partial"})
	n, err := s.PruneHistory(now.AddDate(0, 0, -90))
	if err != nil || n != 0 {
		t.Fatalf("pruned %d (err %v): a partial update is an applied update and must be kept", n, err)
	}
}
