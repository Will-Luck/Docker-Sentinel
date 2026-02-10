package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GiteaLN/Docker-Sentinel/internal/docker"
	"github.com/GiteaLN/Docker-Sentinel/internal/logging"
	"github.com/GiteaLN/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// PolicyChanger handles container recreation for label changes.
// Docker labels are immutable after creation, so changing policy
// requires a stop -> remove -> create -> start cycle.
type PolicyChanger struct {
	docker docker.API
	store  *store.Store
	log    *logging.Logger
}

// NewPolicyChanger creates a PolicyChanger with all dependencies.
func NewPolicyChanger(d docker.API, s *store.Store, log *logging.Logger) *PolicyChanger {
	return &PolicyChanger{docker: d, store: s, log: log}
}

// ChangePolicy recreates a container with a new sentinel.policy label.
// A snapshot is saved first so rollback is possible on failure.
func (p *PolicyChanger) ChangePolicy(ctx context.Context, name, newPolicy string) error {
	// 1. Validate policy value.
	switch docker.Policy(newPolicy) {
	case docker.PolicyAuto, docker.PolicyManual, docker.PolicyPinned:
		// valid
	default:
		return fmt.Errorf("invalid policy: %q", newPolicy)
	}

	// 2. Find container by name.
	containers, err := p.docker.ListContainers(ctx)
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}

	id := findContainerID(containers, name)
	if id == "" {
		return fmt.Errorf("container not found: %s", name)
	}

	// 3. Inspect and save snapshot (rollback safety).
	inspect, err := p.docker.InspectContainer(ctx, id)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", name, err)
	}

	snapshotData, err := json.Marshal(inspect)
	if err != nil {
		return fmt.Errorf("marshal snapshot for %s: %w", name, err)
	}
	_ = p.store.SaveSnapshot(name, snapshotData)

	// 4. Refuse if container is mid-update.
	if maint, _ := p.store.GetMaintenance(name); maint {
		return fmt.Errorf("container %s is currently being updated", name)
	}

	// 5. Clone config with new policy label.
	newConfig := cloneConfig(inspect.Config)
	if newConfig.Labels == nil {
		newConfig.Labels = make(map[string]string)
	}
	newConfig.Labels["sentinel.policy"] = newPolicy

	hostConfig := inspect.HostConfig
	netConfig := rebuildNetworkingConfig(inspect.NetworkSettings)

	// 6. Stop -> remove -> create -> start.
	p.log.Info("changing policy", "name", name, "new_policy", newPolicy)

	if err := p.docker.StopContainer(ctx, id, 30); err != nil {
		p.log.Warn("stop failed during policy change", "name", name, "error", err)
	}

	if err := p.docker.RemoveContainer(ctx, id); err != nil {
		return fmt.Errorf("remove for policy change: %w", err)
	}

	newID, err := p.docker.CreateContainer(ctx, name, newConfig, hostConfig, netConfig)
	if err != nil {
		_ = rollback(ctx, p.docker, name, snapshotData, p.log)
		return fmt.Errorf("create for policy change: %w", err)
	}

	if err := p.docker.StartContainer(ctx, newID); err != nil {
		_ = p.docker.RemoveContainer(ctx, newID)
		_ = rollback(ctx, p.docker, name, snapshotData, p.log)
		return fmt.Errorf("start for policy change: %w", err)
	}

	p.log.Info("policy changed", "name", name, "new_policy", newPolicy, "new_id", truncateID(newID))
	return nil
}

// findContainerID searches containers for one matching the given name and
// returns its ID. Returns empty string if not found.
func findContainerID(containers []container.Summary, name string) string {
	for _, c := range containers {
		if containerName(c) == name {
			return c.ID
		}
	}
	return ""
}
