package engine

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/swarm"
)

// setupScanOne builds a scan of one auto container, nginx, with an update.
func setupScanOne(t *testing.T, labels map[string]string) (*mockDocker, *Updater) {
	t.Helper()
	mock := newMockDocker()
	ref := "localhost:1/nginx:latest"
	labels["sentinel.policy"] = "auto"
	mock.containers = []container.Summary{{ID: "aaa", Names: []string{"/nginx"}, Image: ref, Labels: labels}}
	mock.imageDigests[ref] = "localhost:1/nginx@sha256:old"
	mock.distDigests[ref] = "sha256:new"
	mock.inspectResults["aaa"] = container.InspectResponse{
		ID: "aaa", Name: "/nginx", Image: "sha256:old111",
		Config:          &container.Config{Image: ref, Labels: labels},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}
	u, _ := newTestUpdater(t, mock)
	return mock, u
}

// A pre-update hook that exits 75 skips the update: the scan counts it as
// skipped, not as updated (Gitea #85).
func TestScanCountsHookSkipAsSkipped(t *testing.T) {
	mock, u := setupScanOne(t, map[string]string{"sentinel.hook.pre-update": "exit 75"})
	u.SetHookRunner(hooks.NewRunner(mock, &hookStoreStub{hooks: make(map[string][]hooks.Hook)}, slog.New(slog.NewTextHandler(io.Discard, nil))))
	u.cfg.SetHooksEnabled(true)
	mock.execResults["aaa"] = struct {
		exitCode int
		output   string
	}{exitCode: 75}

	result := u.Scan(context.Background(), ScanScheduled)

	if result.Updated != 0 || result.Skipped != 1 {
		t.Errorf("updated = %d skipped = %d, want the skip counted as skipped", result.Updated, result.Skipped)
	}
}

// A pull that resolves to the image already running changes nothing: the
// container is up to date, not updated.
func TestScanCountsSameImageAsUpToDate(t *testing.T) {
	mock, u := setupScanOne(t, map[string]string{})
	mock.imageIDs["localhost:1/nginx:latest"] = "sha256:old111"

	result := u.Scan(context.Background(), ScanScheduled)

	if result.Updated != 0 || result.UpToDate != 1 {
		t.Errorf("updated = %d up to date = %d, want it counted as up to date", result.Updated, result.UpToDate)
	}
}

// The scan summary in the history prints the skipped count as skipped and
// the rate-limited count as rate limited (it printed the rate-limited
// count under "skipped").
func TestScanSummaryPrintsSkippedAndRateLimited(t *testing.T) {
	mock, u := setupScanOne(t, map[string]string{"sentinel.policy": "pinned"})
	mock.containers[0].Labels = map[string]string{"sentinel.policy": "pinned"}

	u.Scan(context.Background(), ScanScheduled)

	history, err := u.store.ListHistory(10, "")
	if err != nil {
		t.Fatal(err)
	}
	var summary string
	for _, h := range history {
		if h.Outcome == "scan_summary" {
			summary = h.Error
		}
	}
	if !strings.Contains(summary, "1 skipped") || !strings.Contains(summary, "0 rate limited") {
		t.Errorf("summary = %q, want 1 skipped and 0 rate limited", summary)
	}
}

// A container found up to date (updated outside Sentinel) leaves the
// digest: its stale "update available" state is cleared.
func TestScanClearsNotifyStateWhenUpToDate(t *testing.T) {
	mock, u := setupScanOne(t, map[string]string{})
	mock.distDigests["localhost:1/nginx:latest"] = "sha256:old"
	if err := u.store.SetNotifyState("nginx", &store.NotifyState{LastDigest: "sha256:new", LastNotified: time.Now()}); err != nil {
		t.Fatal(err)
	}

	u.Scan(context.Background(), ScanScheduled)

	if st, _ := u.store.GetNotifyState("nginx"); st != nil {
		t.Errorf("notify state = %+v, want it cleared for an up-to-date container", st)
	}
}

// A container that is gone leaves the digest too.
func TestScanPrunesNotifyStateOfRemovedContainers(t *testing.T) {
	_, u := setupScanOne(t, map[string]string{})
	if err := u.store.SetNotifyState("gone", &store.NotifyState{LastDigest: "sha256:x", LastNotified: time.Now()}); err != nil {
		t.Fatal(err)
	}

	u.Scan(context.Background(), ScanScheduled)

	if st, _ := u.store.GetNotifyState("gone"); st != nil {
		t.Errorf("notify state = %+v, want it pruned for a removed container", st)
	}
}

// Every container the summary calls checked lands in exactly one count, so the
// numbers add up. A registry check that fails is its own count (it used to be
// in none, so "checked" exceeded the sum).
func TestScanSummaryCountsAddUp(t *testing.T) {
	mock, u := setupScanOne(t, map[string]string{})
	mock.distErr["localhost:1/nginx:latest"] = io.ErrUnexpectedEOF

	result := u.Scan(context.Background(), ScanScheduled)

	checked := result.Total - result.Skipped
	sum := result.UpToDate + result.Updated + result.Queued + result.RateLimited + result.Failed + result.CheckFailed
	if checked != sum {
		t.Errorf("checked = %d, but the counts add up to %d", checked, sum)
	}
	history, _ := u.store.ListHistory(10, "")
	for _, h := range history {
		if h.Outcome == "scan_summary" && !strings.Contains(h.Error, "1 check failed") {
			t.Errorf("summary = %q, want the failed check counted", h.Error)
		}
	}
}

// Ignored versions, a rolled-back update and a dry run each land in a count too.
func TestScanSummaryCountsAddUpForIgnoredAndDryRun(t *testing.T) {
	_, u := setupScanOne(t, map[string]string{})
	if err := u.store.SaveSetting("dry_run", "true"); err != nil {
		t.Fatal(err)
	}
	result := u.Scan(context.Background(), ScanScheduled)
	checked := result.Total - result.Skipped
	sum := result.UpToDate + result.Updated + result.Queued + result.RateLimited + result.Failed + result.CheckFailed
	if checked != sum {
		t.Errorf("dry run: checked = %d, counts add up to %d (%+v)", checked, sum, result)
	}
}

// Over a mixed scan (local and remote containers, Swarm task containers,
// a failed registry check, an ignored update and dry runs) every container
// the scan looks at lands in exactly one count: Total is the number of
// containers checked or skipped (Swarm tasks are left to their service and
// are in none), and "checked" is the sum of the other counts.
func TestScanCountsAddUpOverAMixedScan(t *testing.T) {
	mock := newMockDocker()
	add := func(id, name string, labels map[string]string, upToDate bool) {
		ref := "localhost:1/" + name + ":latest"
		mock.containers = append(mock.containers, container.Summary{ID: id, Names: []string{"/" + name}, Image: ref, Labels: labels})
		mock.imageDigests[ref] = "localhost:1/" + name + "@sha256:old"
		mock.distDigests[ref] = "sha256:new"
		if upToDate {
			mock.distDigests[ref] = "sha256:old"
		}
		mock.inspectResults[id] = container.InspectResponse{
			ID: id, Name: "/" + name, Config: &container.Config{Image: ref, Labels: labels},
			HostConfig: &container.HostConfig{}, NetworkSettings: &container.NetworkSettings{},
		}
	}
	manual := map[string]string{"sentinel.policy": "manual"}
	add("l1", "uptodate", manual, true)
	add("l2", "queued", manual, false)
	add("l3", "ignored", manual, false)
	add("l4", "task", map[string]string{"com.docker.swarm.task": "", "sentinel.policy": "manual"}, false)
	add("l5", "dry", map[string]string{"sentinel.policy": "auto"}, false)

	u, _ := newTestUpdater(t, mock)
	u.SetSettingsReader(u.store)
	if err := u.store.SaveSetting("dry_run", "true"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ignored", store.ScopedKey("h1", "rign")} {
		if err := u.store.AddIgnoredVersion(name, "sha256:new"); err != nil {
			t.Fatal(err)
		}
	}
	mock.distDigests["localhost:1/rup:latest"] = "sha256:old"
	mock.distDigests["localhost:1/rign:latest"] = "sha256:new"
	mock.distDigests["localhost:1/rdry:latest"] = "sha256:new"
	mock.distErr["localhost:1/rerr:latest"] = io.ErrUnexpectedEOF
	remote := func(name string, labels map[string]string) RemoteContainer {
		return RemoteContainer{Name: name, Image: "localhost:1/" + name + ":latest", ImageDigest: "localhost:1/" + name + "@sha256:old", State: "running", Labels: labels}
	}
	u.SetClusterScanner(&fakeCluster{connected: []string{"h1"}, lists: map[string][]RemoteContainer{"h1": {
		remote("rerr", manual), remote("rup", manual), remote("rign", manual),
		remote("rdry", map[string]string{"sentinel.policy": "auto"}),
		remote("rtask", map[string]string{"com.docker.swarm.task": "", "sentinel.policy": "manual"}),
	}}})

	result := u.Scan(context.Background(), ScanScheduled)

	sum := result.UpToDate + result.Updated + result.Queued + result.RateLimited + result.Failed + result.CheckFailed
	if checked := result.Total - result.Skipped; checked != sum {
		t.Errorf("checked = %d, but the counts add up to %d (%+v)", checked, sum, result)
	}
	if result.Total != 8 {
		t.Errorf("total = %d, want 8 (4 local and 4 remote containers, no Swarm tasks): %+v", result.Total, result)
	}
	if result.CheckFailed != 1 || result.Skipped != 2 || result.Queued != 1 || result.UpToDate != 4 {
		t.Errorf("counts = %+v, want 1 check failed, 2 dry runs skipped, 1 queued, 4 up to date (2 ignored)", result)
	}
}

// A container the scan filters out, or that is pinned, leaves the queue
// (and the digest): its row would otherwise stay there for good, as no
// scan looks at it again.
func TestScanDropsQueueRowsOfFilteredAndPinnedContainers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		filter string
	}{
		{"filtered", map[string]string{}, "nginx"},
		{"pinned", map[string]string{"sentinel.policy": "pinned"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock, u := setupScanOne(t, tc.labels)
			if tc.name == "pinned" {
				mock.containers[0].Labels = map[string]string{"sentinel.policy": "pinned"}
			}
			u.SetSettingsReader(u.store)
			if err := u.store.SaveSetting("filters", tc.filter); err != nil {
				t.Fatal(err)
			}
			u.queue.Add(PendingUpdate{ContainerID: "aaa", ContainerName: "nginx", CurrentImage: "localhost:1/nginx:latest"})
			if err := u.store.SetNotifyState("nginx", &store.NotifyState{LastDigest: "sha256:new", LastNotified: time.Now()}); err != nil {
				t.Fatal(err)
			}

			u.Scan(context.Background(), ScanScheduled)

			if _, queued := u.queue.Get("nginx"); queued {
				t.Error("the queue row stayed for a container the scan skips")
			}
			if st, _ := u.store.GetNotifyState("nginx"); st != nil {
				t.Errorf("notify state = %+v, want it cleared with the queue row", st)
			}
		})
	}
}

// The same for a Swarm service.
func TestScanDropsQueueRowsOfFilteredServices(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{{ID: "svc-1", Spec: svcSpec("api", "fake.local/api:3.0", map[string]string{"sentinel.policy": "pinned"})}}
	u, _ := newSwarmTestUpdater(t, mock)
	u.queue.Add(PendingUpdate{ContainerID: "svc-1", ContainerName: "api", Type: "service"})

	u.Scan(context.Background(), ScanScheduled)

	if _, queued := u.queue.Get("api"); queued {
		t.Error("the queue row stayed for a pinned service")
	}
}
