package engine

import (
	"context"
	"fmt"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/moby/moby/api/types/container"
)

func newTestPolicyChanger(t *testing.T, mock *mockDocker) *PolicyChanger {
	t.Helper()
	s := testStore(t)
	log := logging.New(false)
	return NewPolicyChanger(mock, s, log)
}

func TestChangePolicySuccess(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "abc123", Names: []string{"/myapp"}, Image: "nginx:1.25",
			Labels: map[string]string{"sentinel.policy": "manual"}},
	}
	mock.inspectResults["abc123"] = container.InspectResponse{
		ID:   "abc123",
		Name: "/myapp",
		Config: &container.Config{
			Image:  "nginx:1.25",
			Labels: map[string]string{"sentinel.policy": "manual"},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	pc := newTestPolicyChanger(t, mock)
	err := pc.ChangePolicy(context.Background(), "myapp", "auto")
	if err != nil {
		t.Fatalf("ChangePolicy: %v", err)
	}

	// Verify the lifecycle: stop, remove, create, start.
	if len(mock.stopCalls) != 1 || mock.stopCalls[0] != "abc123" {
		t.Errorf("stopCalls = %v, want [abc123]", mock.stopCalls)
	}
	if len(mock.removeCalls) != 1 || mock.removeCalls[0] != "abc123" {
		t.Errorf("removeCalls = %v, want [abc123]", mock.removeCalls)
	}
	if len(mock.createCalls) != 1 || mock.createCalls[0] != "myapp" {
		t.Errorf("createCalls = %v, want [myapp]", mock.createCalls)
	}
	if len(mock.startCalls) != 1 || mock.startCalls[0] != "new-myapp" {
		t.Errorf("startCalls = %v, want [new-myapp]", mock.startCalls)
	}
}

func TestChangePolicyInvalid(t *testing.T) {
	mock := newMockDocker()
	pc := newTestPolicyChanger(t, mock)

	err := pc.ChangePolicy(context.Background(), "myapp", "bogus")
	if err == nil {
		t.Fatal("expected error for invalid policy")
	}
	if got := err.Error(); got != `invalid policy: "bogus"` {
		t.Errorf("error = %q, want invalid policy message", got)
	}
}

func TestChangePolicyContainerNotFound(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{} // empty

	pc := newTestPolicyChanger(t, mock)
	err := pc.ChangePolicy(context.Background(), "nonexistent", "auto")
	if err == nil {
		t.Fatal("expected error for missing container")
	}
	if got := err.Error(); got != "container not found: nonexistent" {
		t.Errorf("error = %q, want container not found message", got)
	}
}

func TestChangePolicyMidUpdate(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "abc123", Names: []string{"/myapp"}, Image: "nginx:1.25",
			Labels: map[string]string{"sentinel.policy": "manual"}},
	}
	mock.inspectResults["abc123"] = container.InspectResponse{
		ID:   "abc123",
		Name: "/myapp",
		Config: &container.Config{
			Image:  "nginx:1.25",
			Labels: map[string]string{"sentinel.policy": "manual"},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	s := testStore(t)
	log := logging.New(false)
	pc := NewPolicyChanger(mock, s, log)

	// Set maintenance flag before attempting policy change.
	if err := s.SetMaintenance("myapp", true); err != nil {
		t.Fatalf("SetMaintenance: %v", err)
	}

	err := pc.ChangePolicy(context.Background(), "myapp", "auto")
	if err == nil {
		t.Fatal("expected error when container is mid-update")
	}
	if got := err.Error(); got != "container myapp is currently being updated" {
		t.Errorf("error = %q, want currently being updated message", got)
	}

	// Verify no stop/remove/create/start calls were made.
	if len(mock.stopCalls) != 0 {
		t.Errorf("stopCalls = %d, want 0 (should not attempt recreation)", len(mock.stopCalls))
	}
}

func TestChangePolicyCreateFailureRollback(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "abc123", Names: []string{"/myapp"}, Image: "nginx:1.25",
			Labels: map[string]string{"sentinel.policy": "manual"}},
	}
	mock.inspectResults["abc123"] = container.InspectResponse{
		ID:   "abc123",
		Name: "/myapp",
		Config: &container.Config{
			Image:  "nginx:1.25",
			Labels: map[string]string{"sentinel.policy": "manual"},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	// Make CreateContainer fail.
	mock.createErr["myapp"] = fmt.Errorf("disk full")

	pc := newTestPolicyChanger(t, mock)
	err := pc.ChangePolicy(context.Background(), "myapp", "auto")
	if err == nil {
		t.Fatal("expected error from failed create")
	}

	// Rollback should attempt a second create (from snapshot).
	// First create fails ("myapp"), then rollback calls create again ("myapp").
	// Since both use the same name and the error is still set, rollback also fails,
	// but the important thing is that it was attempted.
	if len(mock.createCalls) < 2 {
		t.Errorf("createCalls = %d, want >= 2 (original + rollback attempt)", len(mock.createCalls))
	}
}
