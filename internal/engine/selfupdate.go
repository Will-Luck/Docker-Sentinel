package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// SelfUpdater manages self-update operations using the rename-before-replace
// pattern. It renames the running container, creates a new one with the
// original name, starts it, and then stops the old container (this process) so
// the new instance can take over the BoltDB file lock.
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
	// If the pull fails but the image is already on the host, carry on
	// (supports local builds and registry outages).
	su.log.Info("pulling target image", "image", imageRef)
	if err := su.docker.PullImage(ctx, imageRef); err != nil {
		if _, localErr := su.docker.ImageDigest(ctx, imageRef); localErr != nil {
			return fmt.Errorf("pull image: %w", err)
		}
		su.log.Warn("pull failed but image available locally", "image", imageRef, "pull_error", err)
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

	// 8. Start the new container while the old one is still serving. If the
	// start fails, remove the replacement and give the old container its name
	// back; nothing has been stopped yet.
	su.log.Info("starting replacement container", "id", newID[:12])
	if err := su.docker.StartContainer(ctx, newID); err != nil {
		su.log.Error("start failed, rolling back", "error", err)
		_ = su.docker.RemoveContainer(ctx, newID)
		_ = su.docker.RenameContainer(ctx, selfID, selfName)
		if isPortConflict(err) {
			return fmt.Errorf("start replacement: %w (the running instance still holds the published ports; update it from the host with docker pull and a recreate, or publish no ports and use a reverse proxy or host networking)", err)
		}
		return fmt.Errorf("start replacement: %w", err)
	}

	// 9. Stop the old container, which is this process. Without this it keeps
	// the BoltDB file lock and the replacement exits at startup. A background
	// context is used because the caller's context may be cancelled by the
	// request that triggered the update.
	su.log.Info("self-update: new container running, stopping old container to release the database lock", "id", selfID)
	if err := su.docker.StopContainer(context.Background(), selfID, 10); err != nil {
		su.log.Error("self-update: failed to stop old container", "error", err)
		return fmt.Errorf("replacement started but old container %s could not be stopped: %w", oldName, err)
	}
	return nil
}

// isPortConflict reports whether a container start error is a host port
// collision, which happens when the running instance still publishes the port.
func isPortConflict(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "port is already allocated") || strings.Contains(msg, "address already in use")
}

// oldInstanceRE matches the name a superseded instance is renamed to.
var oldInstanceRE = regexp.MustCompile(`^(.+)-old-\d+$`)

// CleanupOldInstances removes "<name>-old-<unix>" containers left behind by
// earlier self-updates. A container is only removed when it carries the
// sentinel.self label, is not running, and the instance it was replaced by
// (the same name without the suffix) is running. Anonymous volumes are kept.
func (su *SelfUpdater) CleanupOldInstances(ctx context.Context) {
	containers, err := su.docker.ListAllContainers(ctx)
	if err != nil {
		su.log.Debug("old instance cleanup: list containers failed", "error", err)
		return
	}
	running := make(map[string]bool)
	for _, c := range containers {
		if c.State == container.StateRunning && len(c.Names) > 0 {
			running[strings.TrimPrefix(c.Names[0], "/")] = true
		}
	}
	for _, c := range containers {
		if c.Labels["sentinel.self"] != "true" || c.State == container.StateRunning || len(c.Names) == 0 {
			continue
		}
		m := oldInstanceRE.FindStringSubmatch(strings.TrimPrefix(c.Names[0], "/"))
		if m == nil || !running[m[1]] {
			continue
		}
		if err := su.docker.RemoveContainer(ctx, c.ID); err != nil {
			su.log.Warn("old instance cleanup: remove failed", "name", c.Names[0], "error", err)
			continue
		}
		su.log.Info("removed leftover container from an earlier self-update", "name", strings.TrimPrefix(c.Names[0], "/"))
	}
}
