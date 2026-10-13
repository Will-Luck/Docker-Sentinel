package store

import (
	"testing"
	"time"
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

	if key, err := s.FailedUpdate("web"); err != nil || key != "" {
		t.Fatalf("no marker = %q, %v", key, err)
	}
	if err := s.SetFailedUpdate("web", "sha256:abc"); err != nil {
		t.Fatal(err)
	}
	if key, _ := s.FailedUpdate("web"); key != "sha256:abc" {
		t.Errorf("marker = %q", key)
	}
	if err := s.ClearFailedUpdate("web"); err != nil {
		t.Fatal(err)
	}
	if key, _ := s.FailedUpdate("web"); key != "" {
		t.Errorf("marker after clear = %q", key)
	}
}
