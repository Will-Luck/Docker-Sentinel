package engine

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// The digest listed every remote pending update twice (the queue's bare name
// and the notify state's hostID::name), and by raw host ID.
func TestDigestListsRemoteUpdatesOnceWithHostName(t *testing.T) {
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	cn := &capturingNotifier{}
	log := logging.New(false)
	n := notify.NewMulti(log, cn)
	d := NewDigestScheduler(s, q, n, nil, log, newMockClock(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)))

	_ = s.SetNotifyState("h1::web", &store.NotifyState{LastDigest: "x"})
	_ = s.SetNotifyState("db", &store.NotifyState{LastDigest: "y"})
	q.Add(PendingUpdate{HostID: "h1", HostName: "Agent One", ContainerName: "web", CurrentImage: "nginx:1"})
	q.Add(PendingUpdate{ContainerName: "db", CurrentImage: "postgres:16"})

	d.fire(context.Background())

	cn.mu.Lock()
	defer cn.mu.Unlock()
	if len(cn.events) != 1 {
		t.Fatalf("%d digests sent, want 1", len(cn.events))
	}
	want := []string{"db", "web on Agent One"}
	if got := cn.events[0].ContainerNames; !slices.Equal(got, want) {
		t.Errorf("digest lists %v, want %v", got, want)
	}
}

// Remote notify states of containers or hosts that are gone were never
// removed, so the digest kept listing them.
func TestScanPrunesRemoteNotifyStates(t *testing.T) {
	u, _ := newTestUpdater(t, newMockDocker())
	u.SetClusterScanner(&fakeCluster{
		connected: []string{"listed", "failing"},
		removed:   map[string]bool{"gone": true},
		lists:     map[string][]RemoteContainer{"listed": {{Name: "still-here", Image: "nginx:1", ImageDigest: "sha256:old"}}},
		listErr:   map[string]error{"failing": context.DeadlineExceeded},
	})
	for _, k := range []string{"listed::still-here", "listed::removed", "gone::web", "offline::db", "failing::web"} {
		_ = u.store.SetNotifyState(k, &store.NotifyState{LastDigest: "x"})
	}

	u.Scan(context.Background(), ScanScheduled)

	states, _ := u.store.AllNotifyStates()
	for _, keep := range []string{"listed::still-here", "offline::db", "failing::web"} {
		if states[keep] == nil {
			t.Errorf("state %s was pruned although its host did not list or is only offline", keep)
		}
	}
	for _, gone := range []string{"listed::removed", "gone::web"} {
		if states[gone] != nil {
			t.Errorf("state %s survived", gone)
		}
	}
}
