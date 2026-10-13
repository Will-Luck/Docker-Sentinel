package engine

import (
	"context"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/moby/moby/api/types/container"
)

// A container on a mutable tag that runs an older image than the tag (the
// tag was pulled already) is queued with its versions resolved, from its
// own image's digest, as remote containers are.
func TestBehindTagQueueRowHasResolvedVersions(t *testing.T) {
	mock := newMockDocker()
	ref := "localhost:1/lat:latest"
	labels := map[string]string{"sentinel.policy": "manual"}
	mock.containers = []container.Summary{{ID: "aaa", Names: []string{"/lat"}, Image: ref, ImageID: "sha256:img110", Labels: labels}}
	mock.imageDigests[ref] = "localhost:1/lat@sha256:d120"
	mock.distDigests[ref] = "sha256:d120"
	mock.imageIDs[ref] = "sha256:img120" // the tag has moved on
	u, _ := newTestUpdater(t, mock)
	u.docker = &repoDigestDocker{mockDocker: mock, digests: map[string][]string{
		"sha256:img110": {"localhost:1/lat@sha256:d110"},
	}}
	var asked string
	u.resolveVersions = func(_ context.Context, imageRef, digest string, _ docker.SemverScope, _, _ string) registry.CheckResult {
		asked = imageRef + " " + digest
		return registry.CheckResult{ResolvedCurrentVersion: "1.1.0", ResolvedTargetVersion: "1.2.0"}
	}

	u.Scan(context.Background(), ScanScheduled)

	row, ok := u.queue.Get("lat")
	if !ok || row.ResolvedCurrentVersion != "1.1.0" || row.ResolvedTargetVersion != "1.2.0" {
		t.Errorf("queue row = %+v (queued %v), want versions 1.1.0 -> 1.2.0", row, ok)
	}
	if asked != "localhost:1/lat:latest localhost:1/lat@sha256:d110" {
		t.Errorf("resolved with %q, want the container's own image digest", asked)
	}
}
