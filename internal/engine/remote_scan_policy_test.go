package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

func (c *tickingClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// policyCluster is a fakeCluster that also pulls, knows paused hosts and
// can fail updates.
type policyCluster struct {
	*fakeCluster
	paused   map[string]bool
	pulls    []string
	failWith string // the agents' error for every update, with outcome failed
}

func (p *policyCluster) HostPaused(hostID string) bool { return p.paused[hostID] }

func (p *policyCluster) PullImage(_ context.Context, hostID, ref string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pulls = append(p.pulls, hostID+"/"+ref)
	return nil
}

func (p *policyCluster) UpdateContainer(ctx context.Context, hostID, name, target, digest string) (RemoteUpdateResult, error) {
	ur, err := p.fakeCluster.UpdateContainer(ctx, hostID, name, target, digest)
	if p.failWith != "" {
		ur.Outcome, ur.Error = "failed", p.failWith
	}
	return ur, err
}

// capturingNotifier records the events it is sent.
type capturingNotifier struct {
	mu     sync.Mutex
	events []notify.Event
}

func (c *capturingNotifier) Send(_ context.Context, e notify.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	return nil
}
func (c *capturingNotifier) Name() string { return "capture" }

func (c *capturingNotifier) types() []notify.EventType {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []notify.EventType
	for _, e := range c.events {
		out = append(out, e.Type)
	}
	return out
}

// remotePolicyFixture is an updater with auto policy and one remote host,
// h1, running web on nginx:latest with a new digest in the registry.
func remotePolicyFixture(t *testing.T, labels map[string]string) (*Updater, *policyCluster, *tickingClock) {
	t.Helper()
	mock := newMockDocker()
	mock.distDigests["nginx:latest"] = "sha256:new"
	u, _ := newTestUpdater(t, mock)
	clk := &tickingClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	u.clock = clk
	u.cfg.SetDefaultPolicy("auto")
	u.cfg.SetLatestAutoUpdate(false)
	u.SetSettingsReader(u.store)
	pc := &policyCluster{fakeCluster: &fakeCluster{
		connected: []string{"h1"},
		lists: map[string][]RemoteContainer{"h1": {
			{Name: "web", Image: "nginx:latest", ImageDigest: "sha256:old", State: "running", Labels: labels},
		}},
	}}
	u.SetClusterScanner(pc)
	return u, pc, clk
}

// remote-orchestration-3: with an update delay, a remote auto update never
// ran: nothing recorded when the update was first seen. It runs once the
// delay has passed.
func TestScanRemoteUpdateDelay(t *testing.T) {
	u, pc, clk := remotePolicyFixture(t, nil)
	if err := u.store.SaveSetting("update_delay", "1h"); err != nil {
		t.Fatal(err)
	}
	u.Scan(context.Background(), ScanScheduled)
	if len(pc.updates) != 0 {
		t.Fatalf("updated %q on first sight despite the delay", pc.updates)
	}
	clk.Advance(2 * time.Hour)
	u.Scan(context.Background(), ScanScheduled)
	if len(pc.updates) != 1 {
		t.Errorf("updates after the delay = %q, want one", pc.updates)
	}
}

// remote-orchestration-4: remote auto updates ignored the maintenance
// window and the sentinel.schedule label.
func TestScanRemoteMaintenanceWindowAndSchedule(t *testing.T) {
	t.Run("window closed", func(t *testing.T) {
		u, pc, _ := remotePolicyFixture(t, nil)
		if err := u.store.SaveSetting("maintenance_window", "02:00-04:00"); err != nil {
			t.Fatal(err)
		}
		u.Scan(context.Background(), ScanScheduled)
		if len(pc.updates) != 0 {
			t.Errorf("updated %q outside the maintenance window", pc.updates)
		}
	})
	t.Run("schedule not due", func(t *testing.T) {
		u, pc, clk := remotePolicyFixture(t, map[string]string{"sentinel.schedule": "0 3 * * *"})
		u.Scan(context.Background(), ScanScheduled) // first check: due
		n := len(pc.updates)
		pc.fakeCluster.lists["h1"][0].ImageDigest = "sha256:old" // still behind
		clk.Advance(time.Minute)
		u.Scan(context.Background(), ScanScheduled) // checked a minute ago: not due
		if len(pc.updates) != n {
			t.Errorf("checked again a minute later despite sentinel.schedule: updates %q", pc.updates)
		}
	})
}

// remote-orchestration-5: pull-only (global or label) recreated remote
// containers. The agent is asked to pull, nothing more.
func TestScanRemotePullOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		global bool
	}{{"global", nil, true}, {"label", map[string]string{"sentinel.pull-only": "true"}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			u, pc, _ := remotePolicyFixture(t, tc.labels)
			if tc.global {
				if err := u.store.SaveSetting("pull_only", "true"); err != nil {
					t.Fatal(err)
				}
			}
			u.Scan(context.Background(), ScanScheduled)
			if len(pc.updates) != 0 || len(pc.pulls) != 1 || pc.pulls[0] != "h1/nginx:latest" {
				t.Errorf("updates %q, pulls %q; want only a pull of nginx:latest on h1", pc.updates, pc.pulls)
			}
			recs, _ := u.store.ListHistoryByContainer(store.ScopedKey("h1", "web"), 5)
			if len(recs) != 1 || recs[0].Outcome != "pull_only" || recs[0].HostID != "h1" {
				t.Errorf("history = %+v, want one pull_only row for h1", recs)
			}
		})
	}
}

// remote-orchestration-6: a paused host still got updates.
func TestScanRemoteSkipsPausedHosts(t *testing.T) {
	u, pc, _ := remotePolicyFixture(t, nil)
	pc.paused = map[string]bool{"h1": true}
	u.Scan(context.Background(), ScanScheduled)
	if len(pc.updates) != 0 {
		t.Errorf("updated %q on a paused host", pc.updates)
	}
}

// remote-orchestration-8: "Ignore version" on a remote queue row was saved
// for the remote container, but the remote scan never read it.
func TestScanRemoteHonoursIgnoredVersions(t *testing.T) {
	u, pc, _ := remotePolicyFixture(t, nil)
	if err := u.store.AddIgnoredVersion(store.ScopedKey("h1", "web"), "sha256:new"); err != nil {
		t.Fatal(err)
	}
	u.Scan(context.Background(), ScanScheduled)
	if len(pc.updates) != 0 {
		t.Errorf("updated %q to an ignored version", pc.updates)
	}
}

// remote-orchestration-15: remote updates were silent: no notification
// that an update was available, done or failed.
func TestScanRemoteNotifies(t *testing.T) {
	u, pc, _ := remotePolicyFixture(t, nil)
	cn := &capturingNotifier{}
	u.notifier = notify.NewMulti(u.log, cn)
	u.Scan(context.Background(), ScanScheduled)
	got := cn.types()
	want := []notify.EventType{notify.EventUpdateAvailable, notify.EventUpdateStarted, notify.EventUpdateSucceeded}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("notifications = %v, want %v", got, want)
	}

	u2, pc2, _ := remotePolicyFixture(t, nil)
	pc2.failWith = "pull failed"
	cn2 := &capturingNotifier{}
	u2.notifier = notify.NewMulti(u2.log, cn2)
	u2.Scan(context.Background(), ScanScheduled)
	if got := cn2.types(); len(got) != 3 || got[2] != notify.EventUpdateFailed {
		t.Errorf("notifications for a failed remote update = %v, want available, started, failed", got)
	}
	_ = pc
}

// remote-orchestration-18: a failed remote auto update left no history,
// and an agent-reported failure was recorded without its error.
func TestScanRemoteFailuresAreInTheHistory(t *testing.T) {
	u, pc, _ := remotePolicyFixture(t, nil)
	pc.failWith = "pull failed: manifest unknown"
	u.Scan(context.Background(), ScanScheduled)
	recs, _ := u.store.ListHistoryByContainer(store.ScopedKey("h1", "web"), 5)
	if len(recs) != 1 || recs[0].Outcome != "failed" || recs[0].Error != "pull failed: manifest unknown" || recs[0].HostID != "h1" {
		t.Errorf("history = %+v, want one failed row with the agent's error", recs)
	}

	u2, pc2, _ := remotePolicyFixture(t, nil)
	pc2.fakeCluster.updateErr = "agent disconnected"
	u2.Scan(context.Background(), ScanScheduled)
	recs2, _ := u2.store.ListHistoryByContainer(store.ScopedKey("h1", "web"), 5)
	if len(recs2) != 1 || recs2[0].Outcome != "failed" || recs2[0].Error == "" {
		t.Errorf("history after a dispatch error = %+v, want one failed row with the error", recs2)
	}
}

// remote-orchestration-19: the scan's event for a remote update had no
// host ID, so the browser refreshed a local row of the same name.
func TestScanRemoteUpdateEventNamesTheHost(t *testing.T) {
	u, _, _ := remotePolicyFixture(t, nil)
	ch, cancel := subscribeEvents(u)
	defer cancel()
	u.Scan(context.Background(), ScanScheduled)
	for _, e := range drainEvents(ch) {
		if e.ContainerName == "web" && e.Type == events.EventContainerUpdate && e.HostID != "h1" {
			t.Errorf("event %+v has no host ID", e)
		}
	}
}

// subscribeEvents gives the updater an event bus and subscribes to it.
func subscribeEvents(u *Updater) (<-chan events.SSEEvent, func()) {
	if u.events == nil {
		u.events = events.New()
	}
	return u.events.Subscribe()
}

// drainEvents returns the events waiting on ch.
func drainEvents(ch <-chan events.SSEEvent) []events.SSEEvent {
	var out []events.SSEEvent
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

// Remote updates started from the dashboard or the API (approve, update
// now, update to version) notify as scan-started ones do.
func TestNotifyRemote(t *testing.T) {
	u, _, _ := remotePolicyFixture(t, nil)
	cn := &capturingNotifier{}
	u.notifier = notify.NewMulti(u.log, cn)
	u.NotifyRemote(context.Background(), "Agent One", "web", "nginx:1", nil)
	u.NotifyRemote(context.Background(), "Agent One", "web", "nginx:1", &RemoteUpdateResult{Outcome: "success", NewImage: "nginx:2"})
	u.NotifyRemote(context.Background(), "Agent One", "web", "nginx:1", &RemoteUpdateResult{Outcome: "failed", Error: "pull failed"})
	u.NotifyRemote(context.Background(), "Agent One", "web", "nginx:1", &RemoteUpdateResult{Outcome: "skipped"})
	got := cn.types()
	want := []notify.EventType{notify.EventUpdateStarted, notify.EventUpdateSucceeded, notify.EventUpdateFailed}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("notifications = %v, want %v (none for a skip)", got, want)
	}
	cn.mu.Lock()
	defer cn.mu.Unlock()
	if cn.events[0].ContainerName != "web on Agent One" {
		t.Errorf("named %q, want web on Agent One", cn.events[0].ContainerName)
	}
}
