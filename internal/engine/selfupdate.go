package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

// SelfUpdater manages self-update operations via an ephemeral helper container.
type SelfUpdater struct {
	docker docker.API
	log    *logging.Logger
}

// NewSelfUpdater creates a SelfUpdater.
func NewSelfUpdater(d docker.API, log *logging.Logger) *SelfUpdater {
	return &SelfUpdater{docker: d, log: log}
}

// Update performs a self-update by creating an ephemeral helper container.
// The helper pulls the new image, stops/removes the current Sentinel container,
// and recreates it with the same configuration.
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

	// 3. Build the docker run arguments from the inspect config.
	dockerArgs := buildDockerRunArgs(inspect)

	// 4. Create the helper script with all arguments embedded.
	script := fmt.Sprintf(`#!/bin/sh
set -e
echo "=== Sentinel Self-Update Helper ==="
echo "Target: %s"
echo "Image: %s"

echo "Pulling new image..."
docker pull "%s"

echo "Stopping old container..."
docker stop -t 30 "%s" 2>/dev/null || true
docker rm "%s" 2>/dev/null || true

echo "Creating new container..."
docker run -d --name "%s" %s "%s"

echo "Self-update complete!"
`, selfName, imageRef, imageRef, selfName, selfName, selfName, dockerArgs, imageRef)

	// 5. Pull the helper image (docker:cli) — it may not be present locally.
	const helperImage = "docker:cli"
	su.log.Info("pulling helper image", "image", helperImage)
	if err := su.docker.PullImage(ctx, helperImage); err != nil {
		return fmt.Errorf("pull helper image: %w", err)
	}

	// 6. Create the ephemeral helper container.
	helperName := fmt.Sprintf("sentinel-updater-%d", time.Now().Unix())

	helperConfig := &container.Config{
		Image: helperImage,
		Cmd:   []string{"/bin/sh", "-c", script},
		Labels: map[string]string{
			"sentinel.helper": "true",
		},
	}

	helperHostConfig := &container.HostConfig{
		AutoRemove: true,
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: "/var/run/docker.sock",
				Target: "/var/run/docker.sock",
			},
		},
	}

	su.log.Info("creating helper container", "name", helperName)

	helperID, err := su.docker.CreateContainer(ctx, helperName, helperConfig, helperHostConfig, nil)
	if err != nil {
		return fmt.Errorf("create helper: %w", err)
	}

	// 7. Start the helper — it runs independently of Sentinel.
	if err := su.docker.StartContainer(ctx, helperID); err != nil {
		_ = su.docker.RemoveContainer(ctx, helperID)
		return fmt.Errorf("start helper: %w", err)
	}

	su.log.Info("self-update helper started — Sentinel will restart shortly", "helper_id", helperID[:12])
	return nil
}

// buildDockerRunArgs reconstructs docker run flags from an inspect response.
func buildDockerRunArgs(inspect container.InspectResponse) string {
	var parts []string

	// Restart policy.
	if inspect.HostConfig != nil && inspect.HostConfig.RestartPolicy.Name != "" {
		rp := string(inspect.HostConfig.RestartPolicy.Name)
		if inspect.HostConfig.RestartPolicy.MaximumRetryCount > 0 {
			rp += fmt.Sprintf(":%d", inspect.HostConfig.RestartPolicy.MaximumRetryCount)
		}
		parts = append(parts, "--restart "+rp)
	}

	// Environment variables.
	if inspect.Config != nil {
		for _, env := range inspect.Config.Env {
			parts = append(parts, fmt.Sprintf("-e '%s'", shellEscape(env)))
		}
	}

	// Labels.
	if inspect.Config != nil {
		for k, v := range inspect.Config.Labels {
			parts = append(parts, fmt.Sprintf("-l '%s=%s'", shellEscape(k), shellEscape(v)))
		}
	}

	// Port bindings.
	if inspect.HostConfig != nil {
		for containerPort, bindings := range inspect.HostConfig.PortBindings {
			for _, binding := range bindings {
				if binding.HostIP.IsValid() && !binding.HostIP.IsUnspecified() {
					parts = append(parts, fmt.Sprintf("-p %s:%s:%s", binding.HostIP.String(), binding.HostPort, containerPort))
				} else {
					parts = append(parts, fmt.Sprintf("-p %s:%s", binding.HostPort, containerPort))
				}
			}
		}
	}

	// Bind mounts and volumes.
	if inspect.HostConfig != nil {
		for _, bind := range inspect.HostConfig.Binds {
			parts = append(parts, fmt.Sprintf("-v '%s'", shellEscape(bind)))
		}
		for _, m := range inspect.HostConfig.Mounts {
			switch m.Type {
			case mount.TypeBind:
				spec := m.Source + ":" + m.Target
				if m.ReadOnly {
					spec += ":ro"
				}
				parts = append(parts, fmt.Sprintf("-v '%s'", shellEscape(spec)))
			case mount.TypeVolume:
				if m.Source != "" {
					spec := m.Source + ":" + m.Target
					parts = append(parts, fmt.Sprintf("-v '%s'", shellEscape(spec)))
				}
			}
		}
	}

	// Networks (skip default bridge).
	if inspect.NetworkSettings != nil {
		for netName := range inspect.NetworkSettings.Networks {
			if netName != "bridge" {
				parts = append(parts, "--network "+netName)
			}
		}
	}

	return strings.Join(parts, " ")
}

// shellEscape escapes single quotes for safe shell interpolation.
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
