package engine

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// A scan updates a container's dependencies before the container, whatever
// order the daemon lists them in, so a dependent is recreated against the
// updated dependency.
func TestScanUpdatesDependenciesFirst(t *testing.T) {
	mock := newMockDocker()
	for _, c := range []struct {
		id, name string
		labels   map[string]string
	}{
		{"aaa", "app", map[string]string{"sentinel.policy": "auto", "sentinel.depends-on": "db"}},
		{"bbb", "db", map[string]string{"sentinel.policy": "auto"}},
	} {
		ref := "localhost:1/" + c.name + ":latest"
		mock.containers = append(mock.containers, container.Summary{ID: c.id, Names: []string{"/" + c.name}, Image: ref, Labels: c.labels})
		mock.imageDigests[ref] = "localhost:1/" + c.name + "@sha256:old"
		mock.distDigests[ref] = "sha256:new"
		mock.inspectResults[c.id] = container.InspectResponse{
			ID: c.id, Name: "/" + c.name,
			Config:          &container.Config{Image: ref, Labels: c.labels},
			HostConfig:      &container.HostConfig{},
			NetworkSettings: &container.NetworkSettings{},
		}
	}
	u, _ := newTestUpdater(t, mock)

	u.Scan(context.Background(), ScanScheduled)

	want := []string{"localhost:1/db:latest", "localhost:1/app:latest"}
	if !slices.Equal(mock.pullCalls, want) {
		t.Errorf("pulls = %v, want %v (the dependency first)", mock.pullCalls, want)
	}
}

// Updating a network provider first recreates its namespace consumers, so
// a consumer updated later in the same scan has a new ID; the scan uses the
// container's current ID rather than the one it listed.
func TestScanUpdatesConsumerAfterItsProviderRecreatedIt(t *testing.T) {
	f := newFakeDocker()
	auto := map[string]string{"sentinel.policy": "auto"}
	for _, c := range []struct{ id, name, netMode string }{{"vpn", "gluetun", ""}, {"q1", "qbit", "container:vpn"}} {
		ref := "localhost:1/" + c.name + ":latest"
		f.imageDigests[ref] = "localhost:1/" + c.name + "@sha256:old"
		f.distDigests[ref] = "sha256:new"
		f.imageIDs[ref] = "sha256:old-" + c.name // the tag moves when pulled
		f.imageIDs["sha256:old-"+c.name] = "sha256:old-" + c.name
		f.pullResolves[ref] = "sha256:new-" + c.name
		old := f.add(c.id, c.name, ref, true, &container.Config{Labels: maps.Clone(auto)}, &container.HostConfig{NetworkMode: container.NetworkMode(c.netMode)})
		old.imageID = "sha256:old-" + c.name
	}
	u := newFakeUpdater(t, f)

	result := u.Scan(context.Background(), ScanScheduled)

	if result.Updated != 2 || result.Failed != 0 {
		t.Errorf("updated = %d failed = %d (errors %v), want both updated", result.Updated, result.Failed, result.Errors)
	}
	if c := f.byName("qbit"); c == nil || c.imageID != "sha256:new-qbit" || !c.running {
		t.Errorf("containers = %v, want qbit running on its new image", f.names())
	}
}
