package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// consumerOf adds a running consumer that shares providerID's network.
func consumerOf(f *fakeDocker, providerID string) *fakeContainer {
	return f.add("q1", "qbit", "qbit:4", true, nil, &container.HostConfig{NetworkMode: container.NetworkMode("container:" + providerID)})
}

// The finalise step failed but the validated container stays in place
// (with the maintenance label). The provider still changed, so its
// consumers must be moved onto it.
func TestUpdateContainerPartialFinaliseRepairsConsumers(t *testing.T) {
	mock, u := setupUpdateMock(t)
	mock.renameErr["new-nginx"] = fmt.Errorf("name in use") // the finalise swap fails at once
	mock.containers = []container.Summary{
		summary("new-nginx", "nginx", container.StateRunning, nil),
		summary("qbit-id", "qbit", container.StateRunning, nil),
	}
	mock.inspectResults["qbit-id"] = container.InspectResponse{
		ID: "qbit-id", Name: "/qbit", State: &container.State{Running: true},
		Config:     &container.Config{Image: "qbit:4"},
		HostConfig: &container.HostConfig{NetworkMode: "container:aaa"},
	}

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err == nil {
		t.Fatal("expected the finalise failure")
	}
	i := slices.Index(mock.createCalls, "qbit")
	if i < 0 || mock.createHostHistory[i].NetworkMode != "container:new-nginx" {
		t.Errorf("consumer not recreated on the updated provider: createCalls=%v", mock.createCalls)
	}
}

// A failed update that stopped and restarted the old provider leaves its
// consumers in a dead namespace: they are restarted to rejoin it.
func TestUpdateContainerRestoreRestartsConsumers(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	consumerOf(f, "old")
	f.failStart["app-1"] = errors.New("OCI error")

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the start failure")
	}
	assertFakeOps(t, f, "start old", "restart q1")
}

// A dashboard rollback replaces the provider, so its consumers are moved
// onto the rollback container.
func TestRollbackContainerRepairsConsumers(t *testing.T) {
	f := newFakeDocker()
	f.imageIDs["vpn:1"] = "sha256:v1"
	f.add("v2", "gluetun", "vpn:1", true, nil, nil).imageID = "sha256:v2"
	consumerOf(f, "v2")
	u := newFakeUpdater(t, f)
	snap, _ := json.Marshal(container.InspectResponse{
		ID: "v1", Name: "/gluetun", Image: "sha256:v1",
		Config: &container.Config{Image: "vpn:1"}, HostConfig: &container.HostConfig{},
	})
	if err := u.store.SaveSnapshot("gluetun", snap); err != nil {
		t.Fatal(err)
	}

	if err := u.RollbackContainer(context.Background(), "gluetun"); err != nil {
		t.Fatalf("RollbackContainer: %v (containers %v)", err, f.names())
	}
	provider, consumer := f.byName("gluetun"), f.byName("qbit")
	if provider == nil || consumer == nil || consumer.host.NetworkMode != container.NetworkMode("container:"+provider.id) {
		t.Errorf("containers = %v, want qbit recreated on the rollback container", f.names())
	}
}

// Startup recovery that finalises a provider moves its consumers too.
func TestRecoverFinaliseRepairsConsumers(t *testing.T) {
	f := newFakeDocker()
	f.add("n1", "gluetun", "vpn:2", true, &container.Config{Labels: map[string]string{"sentinel.maintenance": "true"}}, nil)
	consumerOf(f, "n1")
	u := newFakeUpdater(t, f)

	u.RecoverInterruptedUpdates(context.Background())

	provider, consumer := f.byName("gluetun"), f.byName("qbit")
	if provider == nil || provider.id == "n1" || consumer == nil || consumer.host.NetworkMode != container.NetworkMode("container:"+provider.id) {
		t.Errorf("containers = %v, want gluetun finalised and qbit moved onto it", f.names())
	}
}

// Startup recovery that finishes a committed swap moves the consumers that
// still reference the removed old provider.
func TestRecoverCommittedSwapRepairsConsumers(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, false, nil)
	consumerOf(f, "old")
	interrupted(t, f, u, store.SwapRecord{Name: "app", OldID: "old", NewID: "new", OldRunning: true, Committed: true}, true, nil)

	u.RecoverInterruptedUpdates(context.Background())

	if c := f.byName("qbit"); c == nil || c.host.NetworkMode != "container:new" {
		t.Errorf("containers = %v, want qbit moved onto the new provider", f.names())
	}
}
