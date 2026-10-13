package store

import (
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestSwapJournalRoundTrip(t *testing.T) {
	s := testStore(t)

	if recs, err := s.ListSwaps(); err != nil || len(recs) != 0 {
		t.Fatalf("empty journal = %v, %v", recs, err)
	}
	rec := SwapRecord{Name: "web", Kind: "update", OldID: "old", OldRunning: true, Started: time.Unix(10, 0).UTC()}
	if err := s.SaveSwap(rec); err != nil {
		t.Fatal(err)
	}
	rec.NewID = "new"
	if err := s.SaveSwap(rec); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSwap(SwapRecord{Name: "db", OldID: "x"}); err != nil {
		t.Fatal(err)
	}

	recs, err := s.ListSwaps()
	if err != nil || len(recs) != 2 || recs[0].Name != "db" || recs[1].NewID != "new" || !recs[1].OldRunning {
		t.Fatalf("ListSwaps = %+v, %v", recs, err)
	}

	if err := s.DeleteSwap("web"); err != nil {
		t.Fatal(err)
	}
	if recs, _ := s.ListSwaps(); len(recs) != 1 || recs[0].Name != "db" {
		t.Errorf("after delete = %+v", recs)
	}
	if err := s.DeleteSwap("missing"); err != nil {
		t.Errorf("deleting a missing record: %v", err)
	}
}

func TestFailedUpdateMarker(t *testing.T) {
	s := testStore(t)

	if _, found, err := s.GetFailedUpdate("web"); err != nil || found {
		t.Fatalf("no marker: found = %v, err = %v", found, err)
	}
	want := FailedUpdate{Version: "1.2", Digest: "sha256:abc"}
	if err := s.SetFailedUpdate("web", want); err != nil {
		t.Fatal(err)
	}
	if got, found, _ := s.GetFailedUpdate("web"); !found || got != want {
		t.Errorf("marker = %+v (found %v), want %+v", got, found, want)
	}
	if names, _ := s.ListFailedUpdates(); len(names) != 1 || names[0] != "web" {
		t.Errorf("ListFailedUpdates = %v", names)
	}
	if err := s.ClearFailedUpdate("web"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.GetFailedUpdate("web"); found {
		t.Error("marker still there after clear")
	}
}

// An unreadable record is skipped by ListSwaps but still reported by
// GetSwap, and DeleteSwap discards it.
func TestUnreadableSwapRecord(t *testing.T) {
	s := testStore(t)
	if err := s.SaveSwap(SwapRecord{Name: "ok", OldID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSwapJournal).Put([]byte("bad"), []byte("{not json"))
	}); err != nil {
		t.Fatal(err)
	}

	if recs, err := s.ListSwaps(); err != nil || len(recs) != 1 || recs[0].Name != "ok" {
		t.Errorf("ListSwaps = %+v, %v", recs, err)
	}
	if _, found, err := s.GetSwap("bad"); !found || err == nil {
		t.Errorf("GetSwap(bad) found = %v err = %v, want found with an error", found, err)
	}
	if !s.HasSwap("bad") {
		t.Error("HasSwap(bad) = false")
	}
	if err := s.DeleteSwap("bad"); err != nil || s.HasSwap("bad") {
		t.Errorf("DeleteSwap(bad) = %v, still there = %v", err, s.HasSwap("bad"))
	}
}
