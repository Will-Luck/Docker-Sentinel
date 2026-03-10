package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/moby/moby/api/types/network"
)

// SelfUpdater manages self-update operations using the rename-before-replace
// pattern. It renames the running container, creates a new one with the
// original name, and starts it. The old container (this process) exits
// naturally after the new one is running.
type SelfUpdater struct {
	docker docker.API
	log    *logging.Logger
}

// NewSelfUpdater creates a SelfUpdater.
func NewSelfUpdater(d docker.API, log *logging.Logger) *SelfUpdater {
	return &SelfUpdater{docker: d, log: log}
}

// Update performs a self-update using rename-before-replace.
// It pulls the new image, renames the current container out of the way,
// creates a new container with the original name and config, connects
// extra networks, and starts it. The old process exits naturally.
func (su *SelfUpdater) Update(ctx context.Context, targetImage string) error {
	// 1. Find our own container.
	containers, err := su.docker.ListContainers(ctx)
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}

	var selfID, selfName string
	for _, c := range containers {
		if c.Labels["sentinel.self"] == "true" {
			selfID = c.ID
			if len(c.Names) > 0 {
				selfName = c.Names[0]
				if len(selfName) > 0 && selfName[0] == '/' {
					selfName = selfName[1:]
				}
			}
			break
		}
	}
	if selfID == "" {
		return fmt.Errorf("could not find sentinel container (no sentinel.self=true label)")
	}

	// 2. Inspect to capture full config.
	inspect, err := su.docker.InspectContainer(ctx, selfID)
	if err != nil {
		return fmt.Errorf("inspect self: %w", err)
	}
	if inspect.Config == nil {
		return fmt.Errorf("inspect %s: container config is nil", selfName)
	}

	imageRef := inspect.Config.Image
	if targetImage != "" {
		imageRef = targetImage
	}
	su.log.Info("self-update initiated", "name", selfName, "image", imageRef)

	// 3. Pull the new image before making any changes.
	su.log.Info("pulling target image", "image", imageRef)
	if err := su.docker.PullImage(ctx, imageRef); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}

	// 4. Rename self out of the way.
	oldName := fmt.Sprintf("%s-old-%d", selfName, time.Now().Unix())
	su.log.Info("renaming self", "from", selfName, "to", oldName)
	if err := su.docker.RenameContainer(ctx, selfID, oldName); err != nil {
		return fmt.Errorf("rename self: %w", err)
	}

	// 5. Build config for the new container from inspect data.
	// Override the image to the target version.
	newConfig := *inspect.Config
	newConfig.Image = imageRef

	// Collect extra networks (all non-bridge, after the first which goes in NetworkingConfig).
	var primaryNetwork string
	var extraNetworks []string
	if inspect.NetworkSettings != nil {
		for netName := range inspect.NetworkSettings.Networks {
			if netName == "bridge" {
				continue
			}
			if primaryNetwork == "" {
				primaryNetwork = netName
			} else {
				extraNetworks = append(extraNetworks, netName)
			}
		}
	}

	var netCfg *network.NetworkingConfig
	if primaryNetwork != "" {
		netCfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				primaryNetwork: {},
			},
		}
	}

	// 6. Create new container with the original name.
	su.log.Info("creating replacement container", "name", selfName, "image", imageRef)
	newID, err := su.docker.CreateContainer(ctx, selfName, &newConfig, inspect.HostConfig, netCfg)
	if err != nil {
		// Rollback: rename back to original name.
		su.log.Error("create failed, rolling back rename", "error", err)
		_ = su.docker.RenameContainer(ctx, selfID, selfName)
		return fmt.Errorf("create replacement: %w", err)
	}

	// 7. Connect extra networks.
	for _, netName := range extraNetworks {
		if err := su.docker.NetworkConnect(ctx, netName, newID); err != nil {
			su.log.Error("failed to connect extra network", "network", netName, "error", err)
			// Non-fatal: container can still start, just missing a secondary network.
		}
	}

	// 8. Start the new container.
	su.log.Info("starting replacement container", "id", newID[:12])
	if err := su.docker.StartContainer(ctx, newID); err != nil {
		// Rollback: remove new container, rename old back.
		su.log.Error("start failed, rolling back", "error", err)
		_ = su.docker.RemoveContainer(ctx, newID)
		_ = su.docker.RenameContainer(ctx, selfID, selfName)
		return fmt.Errorf("start replacement: %w", err)
	}

	// 9. Success. The old container (this process) will exit naturally.
	// The web handler returns 200 before this goroutine completes,
	// and the SSE reconnect logic in the frontend handles the transition.
	su.log.Info("self-update complete — new container running, old process will exit")
	return nil
}
