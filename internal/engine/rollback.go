package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// rollback recreates a container from its snapshot data.
func rollback(ctx context.Context, d docker.API, name string, snapshotData []byte, log *logging.Logger) error {
	var inspect container.InspectResponse
	if err := json.Unmarshal(snapshotData, &inspect); err != nil {
		return fmt.Errorf("unmarshal snapshot: %w", err)
	}

	if inspect.Config == nil {
		return fmt.Errorf("inspect %s: container config is nil", name)
	}

	log.Info("rolling back container", "name", name, "image", inspect.Config.Image)

	// Stop and remove the existing container to free the name.
	containers, err := d.ListAllContainers(ctx)
	if err != nil {
		return fmt.Errorf("list containers for rollback: %w", err)
	}
	for _, c := range containers {
		cName := containerName(c)
		if cName == name {
			log.Info("stopping existing container before rollback", "name", name, "id", truncateID(c.ID))
			if err := d.StopContainer(ctx, c.ID, 30); err != nil {
				log.Warn("stop failed, proceeding with force remove", "name", name, "error", err)
			}
			if err := d.RemoveContainer(ctx, c.ID); err != nil {
				return fmt.Errorf("remove existing container before rollback: %w", err)
			}
			break
		}
	}

	cfg := cloneConfig(inspect.Config)
	hostConfig := inspect.HostConfig
	netConfig := rebuildNetworkingConfig(inspect.NetworkSettings)

	newID, err := d.CreateContainer(ctx, name, cfg, hostConfig, netConfig)
	if err != nil {
		return fmt.Errorf("create rollback container: %w", err)
	}

	if err := d.StartContainer(ctx, newID); err != nil {
		return fmt.Errorf("start rollback container: %w", err)
	}

	log.Info("rollback complete", "name", name, "container_id", truncateID(newID))
	return nil
}

// RollbackFromStore fetches the latest snapshot from BoltDB and performs
// a rollback. Intended for future API/dashboard use.
func RollbackFromStore(ctx context.Context, d docker.API, s *store.Store, name string, log *logging.Logger) error {
	data, err := s.GetLatestSnapshot(name)
	if err != nil {
		return fmt.Errorf("get snapshot: %w", err)
	}
	if data == nil {
		return fmt.Errorf("no snapshot found for %s", name)
	}
	return rollback(ctx, d, name, data, log)
}
