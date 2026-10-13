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
