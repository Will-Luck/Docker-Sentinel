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

// rollback recreates a container from its snapshot data.
func rollback(ctx context.Context, d docker.API, name string, snapshotData []byte, log *logging.Logger) error {
	var inspect container.InspectResponse
	if err := json.Unmarshal(snapshotData, &inspect); err != nil {
		return fmt.Errorf("unmarshal snapshot: %w", err)
	}

	log.Info("rolling back container", "name", name, "image", inspect.Config.Image)

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
