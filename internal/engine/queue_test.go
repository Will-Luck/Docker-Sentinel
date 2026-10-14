package engine

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestQueueAddAndList(t *testing.T) {
	s := testStore(t)
	q := NewQueue(s, nil, nil)

	q.Add(PendingUpdate{ContainerName: "nginx", ContainerID: "aaa"})
	q.Add(PendingUpdate{ContainerName: "redis", ContainerID: "bbb"})

	if q.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", q.Len())
	}

	items := q.List()
	if len(items) != 2 {
		t.Fatalf("List() returned %d items, want 2", len(items))
	}
}

func TestQueueGetAndRemove(t *testing.T) {
	s := testStore(t)
	q := NewQueue(s, nil, nil)

	q.Add(PendingUpdate{ContainerName: "nginx", ContainerID: "aaa", DetectedAt: time.Now()})

	u, ok := q.Get("nginx")
	if !ok {
		t.Fatal("Get(nginx) returned false")
	}
	if u.ContainerID != "aaa" {
		t.Errorf("ContainerID = %q, want aaa", u.ContainerID)
	}

	q.Remove("nginx")
	if q.Len() != 0 {
		t.Errorf("Len() = %d after Remove, want 0", q.Len())
	}
	_, ok = q.Get("nginx")
	if ok {
		t.Error("Get(nginx) returned true after Remove")
	}
}

func TestQueueReplaceExisting(t *testing.T) {
	s := testStore(t)
	q := NewQueue(s, nil, nil)

	q.Add(PendingUpdate{ContainerName: "nginx", RemoteDigest: "sha256:old"})
	q.Add(PendingUpdate{ContainerName: "nginx", RemoteDigest: "sha256:new"})

	if q.Len() != 1 {
		t.Fatalf("Len() = %d, want 1 (should replace)", q.Len())
	}
	u, _ := q.Get("nginx")
	if u.RemoteDigest != "sha256:new" {
		t.Errorf("RemoteDigest = %q, want sha256:new", u.RemoteDigest)
	}
}

func TestQueueKeyLocalContainer(t *testing.T) {
	u := PendingUpdate{ContainerName: "nginx"}
	if got := u.Key(); got != "nginx" {
		t.Errorf("Key() = %q, want %q", got, "nginx")
	}
}

func TestQueueKeyRemoteContainer(t *testing.T) {
	u := PendingUpdate{ContainerName: "nginx", HostID: "host1"}
	if got := u.Key(); got != "host1::nginx" {
		t.Errorf("Key() = %q, want %q", got, "host1::nginx")
	}
}

func TestQueueGetByHostScopedKey(t *testing.T) {
	s := testStore(t)
	q := NewQueue(s, nil, nil)

	q.Add(PendingUpdate{ContainerName: "nginx", HostID: "host1", ContainerID: "aaa"})
	q.Add(PendingUpdate{ContainerName: "nginx", HostID: "host2", ContainerID: "bbb"})

	if q.Len() != 2 {
		t.Fatalf("Len() = %d, want 2 (different hosts should not collide)", q.Len())
	}

	u1, ok := q.Get("host1::nginx")
	if !ok || u1.ContainerID != "aaa" {
		t.Errorf("Get(host1::nginx) = %+v, ok=%v, want ContainerID=aaa", u1, ok)
	}
	u2, ok := q.Get("host2::nginx")
	if !ok || u2.ContainerID != "bbb" {
		t.Errorf("Get(host2::nginx) = %+v, ok=%v, want ContainerID=bbb", u2, ok)
	}

	// Local "nginx" (no host) should not be found.
	_, ok = q.Get("nginx")
	if ok {
		t.Error("Get(nginx) should not find host-scoped entries")
	}
}

func TestQueuePersistence(t *testing.T) {
	s := testStore(t)

	// Add items in one queue instance.
	q1 := NewQueue(s, nil, nil)
	q1.Add(PendingUpdate{ContainerName: "nginx", ContainerID: "aaa"})
	q1.Add(PendingUpdate{ContainerName: "redis", ContainerID: "bbb"})

	// Create a new queue from the same store — should restore.
	q2 := NewQueue(s, nil, nil)
	if q2.Len() != 2 {
		t.Fatalf("restored queue Len() = %d, want 2", q2.Len())
	}
	u, ok := q2.Get("nginx")
	if !ok || u.ContainerID != "aaa" {
		t.Errorf("restored nginx = %+v, ok=%v", u, ok)
	}
}

func TestQueueDropsRemovedConnectorEntriesOnLoad(t *testing.T) {
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	q.Add(PendingUpdate{ContainerName: "nginx", ContainerID: "aaa"})
	q.Add(PendingUpdate{ContainerName: "web", ContainerID: "bbb", HostID: "portainer:p1:3", HostName: "edge"})
	q.Add(PendingUpdate{ContainerName: "db", ContainerID: "ccc", HostID: "agent-1"})

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	q2 := NewQueue(s, nil, log)

	if q2.Len() != 2 {
		t.Fatalf("Len() = %d, want 2 after dropping the portainer entry", q2.Len())
	}
	if _, ok := q2.Get("portainer:p1:3::web"); ok {
		t.Error("portainer entry survived the load")
	}
	if _, ok := q2.Get("agent-1::db"); !ok {
		t.Error("cluster entry was dropped")
	}
	if got := strings.Count(buf.String(), "count=1"); got != 1 {
		t.Errorf("expected one Info line with count=1, log was %q", buf.String())
	}

	// The drop is persisted: a third load sees nothing to drop.
	buf.Reset()
	q3 := NewQueue(s, nil, log)
	if q3.Len() != 2 || buf.Len() != 0 {
		t.Errorf("third load: Len=%d log=%q", q3.Len(), buf.String())
	}
}

// A scan re-adds a pending update every time it finds it. The row keeps
// the time the update was first found while the target is the same (the
// same newer version, or for a mutable tag the same digest), and starts
// again when the target changes. Remote rows (hostID::name) and services
// go through the same Add.
func TestQueueKeepsFirstDetectedWhileTheTargetIsTheSame(t *testing.T) {
	q := NewQueue(testStore(t), nil, nil)
	t1 := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	t2, t3 := t1.Add(6*time.Hour), t1.Add(12*time.Hour)

	for _, tc := range []struct {
		name         string
		first, again PendingUpdate
		changed      PendingUpdate
	}{
		{"version", PendingUpdate{ContainerName: "web", NewerVersions: []string{"1.2"}, DetectedAt: t1},
			PendingUpdate{ContainerName: "web", NewerVersions: []string{"1.2"}, DetectedAt: t2},
			PendingUpdate{ContainerName: "web", NewerVersions: []string{"1.3", "1.2"}, DetectedAt: t3}},
		{"digest", PendingUpdate{ContainerName: "db", RemoteDigest: "sha256:a", DetectedAt: t1},
			PendingUpdate{ContainerName: "db", RemoteDigest: "sha256:a", DetectedAt: t2},
			PendingUpdate{ContainerName: "db", RemoteDigest: "sha256:b", DetectedAt: t3}},
		{"remote", PendingUpdate{HostID: "h1", ContainerName: "web", RemoteDigest: "sha256:a", DetectedAt: t1},
			PendingUpdate{HostID: "h1", ContainerName: "web", RemoteDigest: "sha256:a", DetectedAt: t2},
			PendingUpdate{HostID: "h1", ContainerName: "web", RemoteDigest: "sha256:b", DetectedAt: t3}},
	} {
		q.Add(tc.first)
		q.Add(tc.again)
		if got, _ := q.Get(tc.first.Key()); !got.DetectedAt.Equal(t1) {
			t.Errorf("%s: detected %v after the same update was found again, want the first time %v", tc.name, got.DetectedAt, t1)
		}
		q.Add(tc.changed)
		if got, _ := q.Get(tc.first.Key()); !got.DetectedAt.Equal(t3) {
			t.Errorf("%s: detected %v after the target changed, want %v", tc.name, got.DetectedAt, t3)
		}
	}
}

// A scan that finds the same update again leaves the row's detected time
// as it was.
func TestScanKeepsTheQueueRowsDetectedTime(t *testing.T) {
	mock, u := setupScanOne(t, map[string]string{})
	mock.containers[0].Labels = map[string]string{"sentinel.policy": "manual"}
	clk := u.clock.(*mockClock)

	u.Scan(context.Background(), ScanScheduled)
	first, _ := u.queue.Get("nginx")
	clk.Advance(6 * time.Hour)
	u.Scan(context.Background(), ScanScheduled)
	again, _ := u.queue.Get("nginx")

	if first.DetectedAt.IsZero() || !again.DetectedAt.Equal(first.DetectedAt) {
		t.Errorf("detected %v then %v, want the first time kept", first.DetectedAt, again.DetectedAt)
	}
}
