package store

import (
	"path/filepath"
	"testing"
	"time"
)

// Rows written before v2.17.0 for a remote host used the plain container
// name. The migration moves them to "hostID::name" and leaves local rows
// and rows already keyed that way alone; a second run changes nothing.
func TestMigrateRemoteHistoryKeys(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	for i, rec := range []UpdateRecord{
		{ContainerName: "web", Outcome: "success"},                                   // local
		{ContainerName: "web", Outcome: "success", HostID: "h1", HostName: "one"},    // legacy remote
		{ContainerName: "h2::db", Outcome: "success", HostID: "h2", HostName: "two"}, // already scoped
	} {
		rec.Timestamp = now.Add(time.Duration(i) * time.Second)
		if err := s.RecordUpdate(rec); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.MigrateRemoteHistoryKeys()
	if err != nil || n != 1 {
		t.Fatalf("first run changed %d rows (err %v), want 1", n, err)
	}
	if n, _ := s.MigrateRemoteHistoryKeys(); n != 0 {
		t.Errorf("second run changed %d rows, want 0", n)
	}

	check := func(name string, want int) {
		t.Helper()
		recs, _ := s.ListHistoryByContainer(name, 10)
		if len(recs) != want {
			t.Errorf("rows under %q = %d, want %d", name, len(recs), want)
		}
	}
	check("web", 1)
	check("h1::web", 1)
	check("h2::db", 1)
	if recs, _ := s.ListHistoryByContainer("h1::web", 1); len(recs) == 1 && recs[0].HostName != "one" {
		t.Errorf("migrated row lost its host name: %+v", recs[0])
	}
}
