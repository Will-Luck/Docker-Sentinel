package engine

import (
	"context"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/moby/moby/api/types/swarm"
)

type credStore []registry.RegistryCredential

func (c credStore) GetRegistryCredentials() ([]registry.RegistryCredential, error) { return c, nil }
func (c credStore) SetRegistryCredentials([]registry.RegistryCredential) error     { return nil }

type authCapturingDocker struct {
	*mockDocker
	auths []string
}

func (a *authCapturingDocker) UpdateService(ctx context.Context, id string, v swarm.Version, spec swarm.ServiceSpec, auth string) error {
	a.auths = append(a.auths, auth)
	return a.mockDocker.UpdateService(ctx, id, v, spec, auth)
}

// Swarm nodes pull the new image with the stored credentials of its registry.
func TestUpdateServiceSendsRegistryAuth(t *testing.T) {
	mock := newMockDocker()
	mock.inspectService["svc-1"] = swarm.Service{
		ID: "svc-1", Spec: svcSpec("web", "ghcr.io/org/app:1.0", nil),
		UpdateStatus: &swarm.UpdateStatus{State: swarm.UpdateStateCompleted},
	}
	u, _ := newSwarmTestUpdater(t, mock)
	u.checker.SetCredentialStore(credStore{{Registry: "ghcr.io", Username: "me", Secret: "pat"}})
	cap := &authCapturingDocker{mockDocker: mock}
	u.docker = cap

	_ = u.UpdateService(context.Background(), "svc-1", "web", "ghcr.io/org/app:1.1")

	if len(cap.auths) != 1 || cap.auths[0] == "" {
		t.Errorf("registry auth sent = %q, want the ghcr.io credential", cap.auths)
	}
}
