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
// that host. Rows written before v2.17.0 by an approved update or the
// offline journal carry the plain name plus the host ID; they must count.
// (The host is not connected here, so a rollback that found its history
// fails at the dispatch step instead.)
func TestRollbackRemoteContainer_FindsLegacyApprovedHistory(t *testing.T) {
	a, db := historyAdapter(t)
	if err := db.RecordUpdate(store.UpdateRecord{
		Timestamp: time.Now(), ContainerName: "nginx", OldImage: "nginx:1.25",
		NewImage: "nginx:1.26", Outcome: "success", HostID: "host-1", HostName: "h1",
	}); err != nil {
		t.Fatal(err)
	}

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

	err := a.RollbackRemoteContainer(context.Background(), "host-1", "nginx")
	if err == nil || !strings.Contains(err.Error(), "no successful update history") {
		t.Fatalf("rollback on host-1 used another host's history: %v", err)
	}
}
