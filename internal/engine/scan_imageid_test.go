package engine

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// Once the tag a container was started from moves, Docker reports the
// container's image as an ID. The scan must keep checking it against the
// configured reference instead of treating "sha256:..." as a local image.
var manualLabels = map[string]string{"sentinel.policy": "manual"}

func TestScanChecksConfiguredRefWhenDockerReportsImageID(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "w1", Names: []string{"/worker"}, Image: "sha256:old111", ImageID: "sha256:old111", Labels: manualLabels},
	}
	mock.inspectResults["w1"] = container.InspectResponse{
		Config: &container.Config{Image: "fake.local/app:latest"},
	}
	mock.imageDigests["fake.local/app:latest"] = "fake.local/app@sha256:olddigest"
	mock.distDigests["fake.local/app:latest"] = "sha256:newdigest"

	u, _ := newTestUpdater(t, mock)
	res := u.Scan(context.Background(), ScanScheduled)

	if res.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0 (container dropped out of tracking as a local image)", res.Skipped)
	}
	item, ok := u.queue.Get("worker")
	if !ok {
		t.Fatal("worker was not queued although the registry has a newer digest")
	}
	if item.CurrentImage != "fake.local/app:latest" {
		t.Errorf("CurrentImage = %q, want fake.local/app:latest", item.CurrentImage)
	}
}

// Two containers share a mutable tag; the tag was already moved by pulling for
// the other one. This container still runs the old image, so it is behind its
// tag even though the tag's digest now matches the registry.
func TestScanFlagsContainerBehindItsMovedTag(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "w1", Names: []string{"/worker"}, Image: "fake.local/app:latest", ImageID: "sha256:old111", Labels: manualLabels},
		{ID: "w2", Names: []string{"/web"}, Image: "fake.local/app:latest", ImageID: "sha256:new222", Labels: manualLabels},
	}
	mock.imageDigests["fake.local/app:latest"] = "fake.local/app@sha256:same"
	mock.distDigests["fake.local/app:latest"] = "sha256:same"
	mock.imageIDs["fake.local/app:latest"] = "sha256:new222"

	u, _ := newTestUpdater(t, mock)
	res := u.Scan(context.Background(), ScanScheduled)

	if _, ok := u.queue.Get("worker"); !ok {
		t.Error("worker runs an older image than its tag but was reported up to date")
	}
	if _, ok := u.queue.Get("web"); ok {
		t.Error("web runs the image its tag points at but was queued")
	}
	if res.UpToDate != 1 {
		t.Errorf("UpToDate = %d, want 1 (web only)", res.UpToDate)
	}
}

// Pull-only containers never get recreated, so after the first pull Docker
// reports their image as an ID. They must stay tracked, and a tag that has
// already been pulled must not be pulled again on every scan.
func TestScanPullOnlyStaysTrackedAndDoesNotRepull(t *testing.T) {
	labels := map[string]string{"sentinel.policy": "auto", "sentinel.pull-only": "true"}

	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "p1", Names: []string{"/puller"}, Image: "sha256:old111", ImageID: "sha256:old111", Labels: labels},
	}
	mock.inspectResults["p1"] = container.InspectResponse{
		Config: &container.Config{Image: "fake.local/app:latest", Labels: labels},
	}
	mock.imageDigests["fake.local/app:latest"] = "fake.local/app@sha256:olddigest"
	mock.distDigests["fake.local/app:latest"] = "sha256:newdigest"

	u, _ := newTestUpdater(t, mock)
	u.Scan(context.Background(), ScanScheduled)
	if len(mock.pullCalls) != 1 || mock.pullCalls[0] != "fake.local/app:latest" {
		t.Fatalf("first scan pullCalls = %v, want one pull of fake.local/app:latest", mock.pullCalls)
	}

	// The tag has been pulled: local digest matches the registry, the tag
	// resolves to a newer image than the container runs.
	mock.imageDigests["fake.local/app:latest"] = "fake.local/app@sha256:newdigest"
	mock.imageIDs["fake.local/app:latest"] = "sha256:new222"
	mock.pullCalls = nil
	u.Scan(context.Background(), ScanScheduled)
	if len(mock.pullCalls) != 0 {
		t.Errorf("second scan pulled again: %v", mock.pullCalls)
	}
}

// An ignored update on a mutable tag stays ignored: no notification, no queue
// entry and no auto-update. The ignore key is the version the tag resolves to,
// or the remote digest when that is unknown.
func TestScanHonoursIgnoredMutableTagUpdate(t *testing.T) {
	for _, policy := range []string{"manual", "auto"} {
		t.Run(policy, func(t *testing.T) {
			mock := newMockDocker()
			labels := map[string]string{"sentinel.policy": policy}
			mock.containers = []container.Summary{
				{ID: "w1", Names: []string{"/web"}, Image: "fake.local/app:latest", Labels: labels},
			}
			mock.inspectResults["w1"] = container.InspectResponse{Config: &container.Config{Image: "fake.local/app:latest", Labels: labels}}
			mock.imageDigests["fake.local/app:latest"] = "fake.local/app@sha256:olddigest"
			mock.distDigests["fake.local/app:latest"] = "sha256:newdigest"

			u, _ := newTestUpdater(t, mock)
			if err := u.store.AddIgnoredVersion("web", "sha256:newdigest"); err != nil {
				t.Fatal(err)
			}
			res := u.Scan(context.Background(), ScanScheduled)

			if _, queued := u.queue.Get("web"); queued || res.Queued != 0 || res.AutoCount != 0 || res.Updated != 0 {
				t.Errorf("ignored update was acted on: queued=%v result=%+v", queued, res)
			}
			if len(mock.pullCalls) != 0 {
				t.Errorf("ignored update was pulled: %v", mock.pullCalls)
			}

			// A later digest is a different update and is offered again.
			mock.distDigests["fake.local/app:latest"] = "sha256:evennewer"
			u.Scan(context.Background(), ScanScheduled)
			if policy == "manual" {
				if _, queued := u.queue.Get("web"); !queued {
					t.Error("a new digest should not inherit the ignore")
				}
			}
		})
	}
}
