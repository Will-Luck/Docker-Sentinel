package engine

import (
	"path/filepath"
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
