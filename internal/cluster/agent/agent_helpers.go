package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// sendMsg serialises a message send on the bidirectional stream.
// gRPC stream.Send is not safe for concurrent use, and we have multiple
// goroutines (heartbeat loop, command handlers) that write to the same
// stream. This helper ensures only one Send executes at a time.
func (a *Agent) sendMsg(stream proto.AgentService_ChannelClient, msg *proto.AgentMessage) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	return stream.Send(msg)
}

// heartbeatLoop sends periodic heartbeats to the server. Returns on
// stream error or context cancellation.
func (a *Agent) heartbeatLoop(ctx context.Context, stream proto.AgentService_ChannelClient) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			msg := &proto.AgentMessage{
				Payload: &proto.AgentMessage_Heartbeat{
					Heartbeat: &proto.Heartbeat{
						Timestamp:         timestamppb.Now(),
						AgentVersion:      a.cfg.Version,
						SupportedFeatures: supportedFeatures,
						HostId:            a.hostID,
					},
				},
			}
			if err := a.sendMsg(stream, msg); err != nil {
				return fmt.Errorf("send heartbeat: %w", err)
			}
			a.log.Debug("heartbeat sent")
		}
	}
}

// --- Container Operations ---

// recreateContainer stops, removes, and recreates a container with a new
// image. Preserves all configuration from the current container inspect.
// Returns the old image, old digest, new digest, and any error.
func (a *Agent) recreateContainer(ctx context.Context, name, targetImage string) (oldImage, oldDigest, newDigest string, err error) {
	// Find and inspect the current container.
	cID, err := a.findContainerID(ctx, name)
	if err != nil {
		return "", "", "", fmt.Errorf("find container %s: %w", name, err)
	}

	inspect, err := a.docker.InspectContainer(ctx, cID)
	if err != nil {
		return "", "", "", fmt.Errorf("inspect %s: %w", name, err)
	}

	oldImage = inspect.Config.Image

	// Get current image digest for the audit trail.
	oldDigest, _ = a.docker.ImageDigest(ctx, oldImage)

	// Pull the target image.
	if err := a.docker.PullImage(ctx, targetImage); err != nil {
		return oldImage, oldDigest, "", fmt.Errorf("pull %s: %w", targetImage, err)
	}

	// Get the new image's digest.
	newDigest, _ = a.docker.ImageDigest(ctx, targetImage)

	// Stop the running container. 30s timeout is generous but safe.
	if err := a.docker.StopContainer(ctx, cID, 30); err != nil {
		return oldImage, oldDigest, newDigest, fmt.Errorf("stop %s: %w", name, err)
	}

	// Remove the old container.
	if err := a.docker.RemoveContainer(ctx, cID); err != nil {
		return oldImage, oldDigest, newDigest, fmt.Errorf("remove %s: %w", name, err)
	}

	// Rebuild the container config with the new image. We extract
	// Config, HostConfig, and NetworkingConfig from the inspect result.
	cfg, hostCfg, netCfg := configFromInspect(&inspect, targetImage)

	newID, err := a.docker.CreateContainer(ctx, name, cfg, hostCfg, netCfg)
	if err != nil {
		return oldImage, oldDigest, newDigest, fmt.Errorf("create %s: %w", name, err)
	}

	if err := a.docker.StartContainer(ctx, newID); err != nil {
		return oldImage, oldDigest, newDigest, fmt.Errorf("start %s: %w", name, err)
	}

	return oldImage, oldDigest, newDigest, nil
}

// configFromInspect extracts container creation parameters from an
// InspectResponse, replacing the image with targetImage. This preserves
// env vars, volumes, ports, networks, and all other configuration from
// the original container.
func configFromInspect(inspect *container.InspectResponse, targetImage string) (*container.Config, *container.HostConfig, *network.NetworkingConfig) {
	cfgCopy := *inspect.Config
	cfgCopy.Image = targetImage

	hostCfg := inspect.HostConfig

	// Rebuild NetworkingConfig from the inspect's network settings.
	// Only copy user-specified fields (IPAM, aliases, driver opts).
	// Copying runtime fields (Gateway, IPAddress, etc.) causes conflicts
	// when Docker tries to assign them on the new container.
	netCfg := &network.NetworkingConfig{}
	if inspect.NetworkSettings != nil && len(inspect.NetworkSettings.Networks) > 0 {
		netCfg.EndpointsConfig = make(map[string]*network.EndpointSettings, len(inspect.NetworkSettings.Networks))
		for name, ep := range inspect.NetworkSettings.Networks {
			netCfg.EndpointsConfig[name] = &network.EndpointSettings{
				IPAMConfig: ep.IPAMConfig,
				Aliases:    ep.Aliases,
				DriverOpts: ep.DriverOpts,
				NetworkID:  ep.NetworkID,
				MacAddress: ep.MacAddress,
			}
		}
	}

	return &cfgCopy, hostCfg, netCfg
}

// --- Helpers ---

// listLocalContainers fetches all containers (regardless of state) from the
// local Docker daemon and converts them to proto ContainerInfo messages.
// Using ListAllContainers ensures stopped containers remain visible on the
// dashboard after a stop action.
func (a *Agent) listLocalContainers(ctx context.Context) ([]*proto.ContainerInfo, error) {
	summaries, err := a.docker.ListAllContainers(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*proto.ContainerInfo, 0, len(summaries))
	for i := range summaries {
		// Skip Swarm task containers — they're managed by the Swarm
		// orchestrator and can't be updated through the recreate flow.
		if _, isTask := summaries[i].Labels["com.docker.swarm.task"]; isTask {
			continue
		}
		ci := containerInfoFromSummary(&summaries[i])

		// Populate the image digest so the server can compare against
		// the registry without needing the image locally.
		if digest, err := a.docker.ImageDigest(ctx, summaries[i].Image); err == nil {
			ci.ImageDigest = digest
		}

		out = append(out, ci)
	}
	return out, nil
}

// containerInfoFromSummary converts a Docker container.Summary into a
// proto ContainerInfo suitable for sending over the wire.
func containerInfoFromSummary(c *container.Summary) *proto.ContainerInfo {
	name := ""
	if len(c.Names) > 0 {
		// Docker prefixes names with "/" — strip it for cleanliness.
		name = strings.TrimPrefix(c.Names[0], "/")
	}

	info := &proto.ContainerInfo{
		Id:    c.ID,
		Name:  name,
		Image: c.Image,
		State: string(c.State),
	}

	if len(c.Labels) > 0 {
		info.Labels = c.Labels
	}

	// Extract host-bound port mappings, deduplicating IPv4/IPv6 bindings.
	type portKey struct {
		host, container uint16
		proto           string
	}
	seen := make(map[portKey]bool)
	for _, p := range c.Ports {
		if p.PublicPort == 0 {
			continue
		}
		k := portKey{p.PublicPort, p.PrivatePort, p.Type}
		if seen[k] {
			continue
		}
		seen[k] = true
		info.Ports = append(info.Ports, &proto.PortMapping{
			HostIp:        p.IP.String(),
			HostPort:      uint32(p.PublicPort),
			ContainerPort: uint32(p.PrivatePort),
			Protocol:      p.Type,
		})
	}

	// container.Summary.Created is Unix timestamp (int64).
	if c.Created > 0 {
		info.Created = timestamppb.New(time.Unix(c.Created, 0))
	}

	return info
}

// findContainerID looks up a container by name and returns its ID.
// Uses ListAllContainers so it can locate stopped containers (e.g. to
// start them after a previous stop action).
func (a *Agent) findContainerID(ctx context.Context, name string) (string, error) {
	containers, err := a.docker.ListAllContainers(ctx)
	if err != nil {
		return "", fmt.Errorf("list containers: %w", err)
	}

	for _, c := range containers {
		for _, n := range c.Names {
			// Docker names are prefixed with "/".
			if strings.TrimPrefix(n, "/") == name {
				return c.ID, nil
			}
		}
	}
	return "", fmt.Errorf("container %q not found", name)
}

// clampInt32 clamps an int to the int32 range. Docker exit codes are
// 0-255 so this is purely defensive.
func clampInt32(v int) int32 {
	const (
		maxInt32 = 1<<31 - 1
		minInt32 = -1 << 31
	)
	if v > maxInt32 {
		return maxInt32
	}
	if v < minInt32 {
		return minInt32
	}
	return int32(v) //nolint:gosec // bounds checked above
}

// --- Dedup ---

// dedup tracks recently processed request IDs to prevent duplicate
// execution on reconnection or message replay. Entries are automatically
// cleaned up after 5 minutes.
type dedup struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	maxSize int
}

func newDedup(maxSize int) *dedup {
	return &dedup{
		seen:    make(map[string]time.Time),
		maxSize: maxSize,
	}
}

// isSeen checks if a request ID has been processed recently. If not, it
// marks it as seen and returns false. Thread-safe.
func (d *dedup) isSeen(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.seen[id]; ok {
		return true
	}

	d.seen[id] = time.Now()

	// Periodic cleanup — run when map is getting large.
	if len(d.seen) > d.maxSize {
		d.cleanup()
	}

	return false
}

// cleanup removes entries older than 5 minutes. Must be called with
// d.mu held.
func (d *dedup) cleanup() {
	cutoff := time.Now().Add(-5 * time.Minute)
	for id, t := range d.seen {
		if t.Before(cutoff) {
			delete(d.seen, id)
		}
	}
}

// --- Backoff ---

// backoff implements exponential backoff for reconnection attempts.
// Caps at maxDelay.
type backoff struct {
	attempt  int
	base     time.Duration
	maxDelay time.Duration
}

func newBackoff() *backoff {
	return &backoff{
		base:     1 * time.Second,
		maxDelay: 30 * time.Second,
	}
}

// next returns the next backoff delay and increments the attempt counter.
// Sequence: 1s, 2s, 4s, 8s, 16s, 30s, 30s, ...
func (b *backoff) next() time.Duration {
	// Cap the shift to avoid overflow. 30 shifts on a nanosecond base
	// already exceeds any reasonable delay, and we clamp to maxDelay
	// anyway.
	shift := b.attempt
	if shift > 30 {
		shift = 30
	}
	delay := b.base << uint(shift) //nolint:gosec // capped above
	if delay > b.maxDelay || delay < 0 {
		delay = b.maxDelay
	}
	b.attempt++
	return delay
}

// reset clears the attempt counter after a successful long-running session.
func (b *backoff) reset() {
	b.attempt = 0
}
