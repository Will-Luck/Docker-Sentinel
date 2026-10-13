package engine

import (
	"context"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/moby/moby/api/types/container"
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

// An ignored update leaves the queue, so it must leave the digest too: its
// notify state used to stay and the digest listed more than the queue held.
func TestIgnoredUpdateLeavesTheDigest(t *testing.T) {
	check := func(t *testing.T, u *Updater, key string) {
		t.Helper()
		u.Scan(context.Background(), ScanScheduled)
		p, ok := u.queue.Get(key)
		if !ok {
			t.Fatalf("%s not queued", key)
		}
		if st, _ := u.store.GetNotifyState(key); st == nil {
			t.Fatalf("no notify state for %s after the first scan", key)
		}
		if err := u.store.AddIgnoredVersion(key, ignoreKey(registry.CheckResult{ResolvedTargetVersion: p.ResolvedTargetVersion, RemoteDigest: p.RemoteDigest})); err != nil {
			t.Fatal(err)
		}
		u.queue.Remove(key) // what the Ignore button does
		u.Scan(context.Background(), ScanScheduled)
		if _, queued := u.queue.Get(key); queued {
			t.Fatalf("%s back in the queue although ignored", key)
		}
		states, _ := u.store.AllNotifyStates()
		for k := range states {
			if k == key {
				t.Errorf("notify state %s survived the ignore: the digest would list it, the queue does not", k)
			}
		}
	}
	t.Run("local", func(t *testing.T) {
		mock := newMockDocker()
		mock.containers = []container.Summary{{ID: "aaa", Names: []string{"/myapp"}, Image: "fake.local/app:1.0"}}
		mock.imageDigests["fake.local/app:1.0"] = "fake.local/app@sha256:old"
		mock.distDigests["fake.local/app:1.0"] = "sha256:new"
		u, _ := newTestUpdater(t, mock)
		check(t, u, "myapp")
	})
	t.Run("remote", func(t *testing.T) {
		u, _, _ := remotePolicyFixture(t, map[string]string{"sentinel.policy": "manual"})
		check(t, u, "h1::web")
	})
}
