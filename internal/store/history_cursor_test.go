package store

import (
	"testing"
	"time"
)

// A cursor taken from a record's own JSON timestamp carries the local offset
// the record was written with; paging must still move strictly backwards.
func TestListHistoryCursorWithTimezoneOffset(t *testing.T) {
	s := testStore(t)
	zone := time.FixedZone("BST", 3600)
	base := time.Date(2026, 10, 13, 12, 0, 0, 0, zone)
	for i := 0; i < 6; i++ {
		if err := s.RecordUpdate(UpdateRecord{
			Timestamp:     base.Add(-time.Duration(i) * 20 * time.Minute),
			ContainerName: "c",
			Outcome:       "success",
		}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[time.Time]bool{}
	cursor := ""
	var all []UpdateRecord
	for page := 0; page < 10; page++ {
		recs, err := s.ListHistory(2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) == 0 {
			break
		}
		for _, r := range recs {
			if seen[r.Timestamp.UTC()] {
				t.Fatalf("page %d repeats %s", page, r.Timestamp)
			}
			seen[r.Timestamp.UTC()] = true
			all = append(all, r)
		}
		// Exactly what the History page does: use the record's JSON timestamp.
		cursor = recs[len(recs)-1].Timestamp.Format(time.RFC3339Nano)
	}
	if len(all) != 6 {
		t.Fatalf("paged through %d of 6 records", len(all))
	}
}

func TestListHistoryCursorWithNegativeOffset(t *testing.T) {
	s := testStore(t)
	zone := time.FixedZone("EST", -5*3600)
	base := time.Date(2026, 10, 13, 12, 0, 0, 0, zone)
	for i := 0; i < 6; i++ {
		_ = s.RecordUpdate(UpdateRecord{Timestamp: base.Add(-time.Duration(i) * 90 * time.Minute), ContainerName: "c", Outcome: "success"})
	}
	var n int
	cursor := ""
	for page := 0; page < 10; page++ {
		recs, _ := s.ListHistory(2, cursor)
		if len(recs) == 0 {
			break
		}
		n += len(recs)
		cursor = recs[len(recs)-1].Timestamp.Format(time.RFC3339Nano)
	}
	if n != 6 {
		t.Fatalf("paged through %d of 6 records (records skipped)", n)
	}
}
