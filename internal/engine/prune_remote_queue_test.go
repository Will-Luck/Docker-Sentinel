package engine

import (
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// rc7: when the hub learns a host's containers anew (after an agent's
// update of itself), the queued updates of containers gone from that host
// leave the queue at once, not at the next scan. Other hosts' entries and
// local ones stay.
func TestPruneRemoteQueue_DropsWhatLeftTheHost(t *testing.T) {
	u, _ := newTestUpdater(t, newMockDocker())
	for _, p := range []PendingUpdate{
		{ContainerName: "sentinel-agent-old-1791990001", HostID: "h1"},
		{ContainerName: "web", HostID: "h1"},
		{ContainerName: "sentinel-agent-old-1791990001", HostID: "h2"},
		{ContainerName: "local-app"},
	} {
		u.queue.Add(p)
	}

	u.PruneRemoteQueue("h1", []string{"web", "sentinel-agent"})

	var keys []string
	for _, p := range u.queue.List() {
		keys = append(keys, p.Key())
	}
	for _, want := range []string{store.ScopedKey("h1", "web"), store.ScopedKey("h2", "sentinel-agent-old-1791990001"), "local-app"} {
		if _, ok := u.queue.Get(want); !ok {
			t.Errorf("queue %v lost %s", keys, want)
		}
	}
	if _, ok := u.queue.Get(store.ScopedKey("h1", "sentinel-agent-old-1791990001")); ok {
		t.Errorf("queue %v still holds the container gone from h1", keys)
	}
}
