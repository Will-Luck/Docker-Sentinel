package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	clusterserver "github.com/Will-Luck/Docker-Sentinel/internal/cluster/server"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

func historyAdapter(t *testing.T) (*clusterAdapter, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ca, err := cluster.EnsureCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := clusterserver.New(ca, db, events.New(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return &clusterAdapter{srv: srv, store: db}, db
}

// Remote rollback needs the last successful update of that container on
// that host. Rows written before v3.0.0 by an approved update or the
// offline journal carry the plain name plus the host ID; after the startup
// migration they must count. (The host is not connected here, so a
// rollback that found its history fails at the dispatch step instead.)
func TestRollbackRemoteContainer_FindsLegacyApprovedHistory(t *testing.T) {
	a, db := historyAdapter(t)
	if err := db.RecordUpdate(store.UpdateRecord{
		Timestamp: time.Now(), ContainerName: "nginx", OldImage: "nginx:1.25",
		NewImage: "nginx:1.26", Outcome: "success", HostID: "host-1", HostName: "h1",
	}); err != nil {
		t.Fatal(err)
	}
	migrateRemoteHistory(db, slog.New(slog.DiscardHandler))

	err := a.RollbackRemoteContainer(context.Background(), "host-1", "nginx")
	if err == nil || strings.Contains(err.Error(), "no successful update history") {
		t.Fatalf("rollback did not find the approved remote update: %v", err)
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("rollback failed for another reason: %v", err)
	}
}

// A same-named container on another host, or a local one, is not this
// container's history.
func TestRollbackRemoteContainer_IgnoresOtherHostsHistory(t *testing.T) {
	a, db := historyAdapter(t)
	for _, rec := range []store.UpdateRecord{
		{Timestamp: time.Now(), ContainerName: "nginx", OldImage: "nginx:1.20", Outcome: "success"},
		{Timestamp: time.Now().Add(time.Second), ContainerName: "nginx", OldImage: "nginx:1.21", Outcome: "success", HostID: "host-2"},
		{Timestamp: time.Now().Add(2 * time.Second), ContainerName: store.ScopedKey("host-2", "nginx"), OldImage: "nginx:1.22", Outcome: "success", HostID: "host-2"},
	} {
		if err := db.RecordUpdate(rec); err != nil {
			t.Fatal(err)
		}
	}

	migrateRemoteHistory(db, slog.New(slog.DiscardHandler))

	err := a.RollbackRemoteContainer(context.Background(), "host-1", "nginx")
	if err == nil || !strings.Contains(err.Error(), "no successful update history") {
		t.Fatalf("rollback on host-1 used another host's history: %v", err)
	}
}

// With a migrated old row and a new row for the same container, the most
// recent successful update decides the rollback image, whichever kind it
// was.
func TestLastRemoteOldImage_NewestOfMigratedAndNewRows(t *testing.T) {
	for _, tc := range []struct {
		name               string
		legacyAt, scopedAt time.Duration
		want               string
	}{
		{"new row is newer", 0, time.Minute, "nginx:1.25"},
		{"old row is newer", time.Minute, 0, "nginx:1.24"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, db := historyAdapter(t)
			now := time.Now()
			for _, rec := range []store.UpdateRecord{
				{Timestamp: now.Add(tc.legacyAt), ContainerName: "nginx", OldImage: "nginx:1.24", Outcome: "success", HostID: "host-1"},
				{Timestamp: now.Add(tc.scopedAt), ContainerName: store.ScopedKey("host-1", "nginx"), OldImage: "nginx:1.25", Outcome: "success", HostID: "host-1"},
			} {
				if err := db.RecordUpdate(rec); err != nil {
					t.Fatal(err)
				}
			}
			migrateRemoteHistory(db, slog.New(slog.DiscardHandler))
			got, err := lastRemoteOldImage(db, "host-1", "nginx")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("rollback image = %q, want %q", got, tc.want)
			}
		})
	}
}

// A rollback row says which image the container left, but it is not an
// update to undo: Rollback picks the last forward update.
func TestLastRemoteOldImage_SkipsRollbackRows(t *testing.T) {
	_, db := historyAdapter(t)
	key := store.ScopedKey("host-1", "nginx")
	now := time.Now()
	for _, rec := range []store.UpdateRecord{
		{Timestamp: now, ContainerName: key, OldImage: "nginx:1.25", NewImage: "nginx:1.26", Outcome: "success", HostID: "host-1"},
		// A rollback row from before v3.0.0, then one as a local rollback
		// row is: both are skipped.
		{Timestamp: now.Add(time.Minute), ContainerName: key, OldImage: "nginx:1.26", NewImage: "nginx:1.25", Outcome: "success", HostID: "host-1", Type: "rollback"},
		{Timestamp: now.Add(2 * time.Minute), ContainerName: key, OldImage: "nginx:1.26", NewImage: "nginx:1.25", Outcome: "rollback", HostID: "host-1"},
	} {
		if err := db.RecordUpdate(rec); err != nil {
			t.Fatal(err)
		}
	}
	got, err := lastRemoteOldImage(db, "host-1", "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if got != "nginx:1.25" {
		t.Errorf("rollback image = %q, want nginx:1.25 from the forward update", got)
	}
}

// A container that was just rolled back has nothing to roll back to; with an
// older update behind it the target is that update's old image. The same rule
// decides whether the dashboard offers the button.
func TestLastRemoteOldImage_AfterAManualRollback(t *testing.T) {
	_, db := historyAdapter(t)
	key := store.ScopedKey("host-1", "nginx")
	now := time.Now()
	rec := func(at time.Duration, old, nw, outcome, errText string) store.UpdateRecord {
		return store.UpdateRecord{Timestamp: now.Add(at), ContainerName: key, OldImage: old, NewImage: nw, Outcome: outcome, Error: errText, HostID: "host-1"}
	}
	for _, r := range []store.UpdateRecord{
		rec(0, "nginx:1.24", "nginx:1.25", "success", ""),
		rec(time.Minute, "nginx:1.25", "nginx:1.26", "success", ""),
		rec(2*time.Minute, "nginx:1.26", "nginx:1.25", "rollback", cluster.RolledBackOnRequest),
	} {
		if err := db.RecordUpdate(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := lastRemoteOldImage(db, "host-1", "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if got != "nginx:1.24" {
		t.Errorf("after rolling back 1.26 to 1.25 the target = %q, want nginx:1.24", got)
	}
	if err := db.RecordUpdate(rec(3*time.Minute, "nginx:1.25", "nginx:1.24", "rollback", cluster.RolledBackOnRequest)); err != nil {
		t.Fatal(err)
	}
	if got, _ := lastRemoteOldImage(db, "host-1", "nginx"); got != "" {
		t.Errorf("after rolling back twice the target = %q, want none", got)
	}
}
