package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"
)

// specCapturingDocker records the specs submitted to UpdateService and, like a
// real manager, starts a fresh rollout (a new UpdateStatus) when the spec changed.
type specCapturingDocker struct {
	*mockDocker
	specs      []swarm.ServiceSpec
	startsNew  bool
	newStarted time.Time
}

func (c *specCapturingDocker) UpdateService(ctx context.Context, id string, v swarm.Version, spec swarm.ServiceSpec, auth string) error {
	cp := *spec.TaskTemplate.ContainerSpec
	spec.TaskTemplate.ContainerSpec = &cp
	c.specs = append(c.specs, spec)
	if c.startsNew {
		svc := c.mockDocker.inspectService[id]
		started := c.newStarted
		svc.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStateCompleted, StartedAt: &started}
		c.mockDocker.inspectService[id] = svc
	}
	return c.mockDocker.UpdateService(ctx, id, v, spec, auth)
}

// steppingClock advances by the requested delay on every After call, so a
// poll that never sees a fresh rollout reaches its timeout without sleeping.
type steppingClock struct{ *mockClock }

func (c steppingClock) After(d time.Duration) <-chan time.Time {
	c.Advance(d)
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

const swarmOldPin = "nginx:latest@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newDigestOnlyFixture(t *testing.T, startsNew bool) (*Updater, *specCapturingDocker, *mockDocker) {
	t.Helper()
	mock := newMockDocker()
	staleStart := time.Date(2025, 12, 1, 9, 0, 0, 0, time.UTC)
	spec := svcSpec("web", swarmOldPin, nil)
	spec.TaskTemplate.ForceUpdate = 4
	mock.inspectService["svc-1"] = swarm.Service{
		ID:           "svc-1",
		Meta:         swarm.Meta{Version: swarm.Version{Index: 3}},
		Spec:         spec,
		UpdateStatus: &swarm.UpdateStatus{State: swarm.UpdateStateCompleted, StartedAt: &staleStart},
	}
	mock.distDigests["nginx:latest"] = "sha256:cccc"
	u, clk := newSwarmTestUpdater(t, mock)
	u.clock = steppingClock{clk}
	capture := &specCapturingDocker{mockDocker: mock, startsNew: startsNew, newStarted: clk.Now().Add(time.Second)}
	u.docker = capture
	return u, capture, mock
}

// A digest-only update (empty target) must not resubmit the old digest pin: the
// manager sees an unchanged spec, rolls nothing out and the update is a no-op.
func TestUpdateServiceDigestOnlyRepinsToRegistryDigest(t *testing.T) {
	u, capture, mock := newDigestOnlyFixture(t, true)
	mock.distDigests["nginx:latest"] = "sha256:bbbb"

	if err := u.UpdateService(context.Background(), "svc-1", "web", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capture.specs) != 1 {
		t.Fatalf("UpdateService calls = %d, want 1", len(capture.specs))
	}
	got := capture.specs[0].TaskTemplate
	if want := "nginx:latest@sha256:bbbb"; got.ContainerSpec.Image != want {
		t.Errorf("submitted image = %q, want %q", got.ContainerSpec.Image, want)
	}
	if got.ForceUpdate != 5 {
		t.Errorf("ForceUpdate = %d, want 5 (bumped so the rollout happens even if the pin is unchanged)", got.ForceUpdate)
	}
}

// When the new digest cannot be looked up (private registry, outage) the
// update fails with a clear error and the spec is left alone: submitting the
// bare tag would drop the digest pin and later scans would lose the service's
// digest for good.
func TestUpdateServiceDigestOnlyFailsWhenDigestLookupFails(t *testing.T) {
	u, capture, mock := newDigestOnlyFixture(t, true)
	mock.distErr["nginx:latest"] = fmt.Errorf("unauthorized")

	err := u.UpdateService(context.Background(), "svc-1", "web", "")
	if err == nil {
		t.Fatal("expected an error when the registry digest cannot be resolved")
	}
	if !strings.Contains(err.Error(), "unauthorized") || !strings.Contains(err.Error(), "digest") {
		t.Errorf("error %q should name the digest lookup and carry the cause", err)
	}
	if len(capture.specs) != 0 {
		t.Errorf("UpdateService was called %d times; the spec must be left unchanged", len(capture.specs))
	}
}

// A completed status left over from an earlier rollout is not this update's result.
func TestUpdateServiceIgnoresUpdateStatusFromEarlierRollout(t *testing.T) {
	u, _, _ := newDigestOnlyFixture(t, false)

	err := u.UpdateService(context.Background(), "svc-1", "web", "")
	if err == nil {
		t.Fatal("update reported success from the previous rollout's completed status")
	}
	records, _ := u.store.ListHistory(10, "")
	if len(records) == 0 || records[0].Outcome != "timeout" {
		t.Errorf("history = %+v, want a timeout record", records)
	}
}

// A targeted (version bump) update leaves ForceUpdate alone.
func TestUpdateServiceTargetedUpdateKeepsForceUpdate(t *testing.T) {
	u, capture, _ := newDigestOnlyFixture(t, true)

	if err := u.UpdateService(context.Background(), "svc-1", "web", "nginx:1.29.2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := capture.specs[0].TaskTemplate
	if got.ContainerSpec.Image != "nginx:1.29.2" || got.ForceUpdate != 4 {
		t.Errorf("image=%q ForceUpdate=%d, want nginx:1.29.2 and 4", got.ContainerSpec.Image, got.ForceUpdate)
	}
}
