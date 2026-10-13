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

// recreateResult describes a finished (or failed) container recreate.
type recreateResult struct {
	oldImage, oldDigest string
	newImage, newDigest string
}

// targetOrCurrent returns target, or the container's own image reference
// when target is empty. Hubs send an empty target for a digest-only update
// (same tag, new image behind it), and the update then re-pulls that tag.
func targetOrCurrent(target string, inspect *container.InspectResponse) string {
	if target != "" || inspect.Config == nil {
		return target
	}
	return inspect.Config.Image
}

// parkedName is the name a container is moved to while its replacement is
// built. The local engine uses the same suffix.
func parkedName(name string) string {
	return name + "-sentinel-old"
}

// recreateContainer replaces a container with one built from the same
// configuration on targetImage, and never leaves the host without it:
//
//  1. pull the image and build the new config (nothing is touched yet)
//  2. park the old container under parkedName (it keeps running)
//  3. create the replacement under the real name
//  4. stop the old container and start the replacement
//  5. remove the old container
//
// If step 3 or 4 fails, the replacement is removed, the old container gets
// its name back and is started again. A container that was not running is
// neither stopped nor started: it comes back stopped, updated or not. An
// empty targetImage re-pulls the container's current image.
func (a *Agent) recreateContainer(ctx context.Context, name, targetImage string) (recreateResult, error) {
	var r recreateResult

	// Find and inspect the current container.
	cID, err := a.findContainerID(ctx, name)
	if err != nil {
		return r, fmt.Errorf("find container %s: %w", name, err)
	}

	inspect, err := a.docker.InspectContainer(ctx, cID)
	if err != nil {
		return r, fmt.Errorf("inspect %s: %w", name, err)
	}

	targetImage = targetOrCurrent(targetImage, &inspect)
	r.newImage = targetImage
	r.oldImage = inspect.Config.Image

	// Get current image digest for the audit trail.
	r.oldDigest, _ = a.docker.ImageDigest(ctx, r.oldImage)

	// Pull the target image. If pull fails but the image is already
	// available locally, continue (supports local/dev images).
	if err := a.docker.PullImage(ctx, targetImage); err != nil {
		if _, localErr := a.docker.ImageDigest(ctx, targetImage); localErr != nil {
			return r, fmt.Errorf("pull %s: %w", targetImage, err)
		}
		a.log.Warn("pull failed but image available locally", "image", targetImage, "pull_error", err)
	}

	// Get the new image's digest.
	r.newDigest, _ = a.docker.ImageDigest(ctx, targetImage)

	spec := buildRecreateSpec(&inspect, targetImage)

	parked := parkedName(name)
	if id, err := a.findContainerID(ctx, parked); err == nil {
		return r, fmt.Errorf("container %s (%s) is left over from an interrupted update; remove it, or rename it to %s if %s is missing, then retry",
			parked, truncateID(id), name, name)
	}
	if err := a.docker.RenameContainer(ctx, cID, parked); err != nil {
		return r, fmt.Errorf("rename %s to %s: %w", name, parked, err)
	}
	old := parkedContainer{id: cID, name: name, inspect: &inspect, targetImage: targetImage, wasRunning: isRunning(&inspect)}

	newID, err := a.createFromSpec(ctx, name, spec)
	if err != nil {
		a.restoreParked(ctx, old, "")
		return r, fmt.Errorf("create %s: %w", name, err)
	}

	if old.wasRunning {
		// Stop before start: the replacement needs the old container's
		// ports. 30s timeout is generous but safe.
		if err := a.docker.StopContainer(ctx, cID, 30); err != nil {
			a.restoreParked(ctx, old, newID)
			return r, fmt.Errorf("stop %s: %w", name, err)
		}

		if err := a.docker.StartContainer(ctx, newID); err != nil {
			a.restoreParked(ctx, old, newID)
			return r, fmt.Errorf("start %s: %w", name, err)
		}
	}

	// A --rm container was already deleted by the daemon when it stopped.
	if err := a.docker.RemoveContainer(ctx, cID); err != nil && !autoRemoves(&inspect) {
		a.log.Warn("container updated, but the old one could not be removed",
			"container", name, "old_container", parked, "error", err)
	}

	return r, nil
}

// recreateSpec is everything needed to create a container like an existing
// one: the config to create it with and the networks to attach.
//
// buildRecreateSpec is the one place the agent turns an inspected container
// into create parameters. The local engine's config helpers (sanitising for
// network_mode "container:", carrying anonymous volumes) belong here when
// they land, so remote and local recreates build containers the same way.
type recreateSpec struct {
	cfg     *container.Config
	hostCfg *container.HostConfig
	nets    networkPlan
}

func buildRecreateSpec(inspect *container.InspectResponse, image string) recreateSpec {
	cfg, hostCfg := configFromInspect(inspect, image)
	return recreateSpec{cfg: cfg, hostCfg: hostCfg, nets: buildNetworkPlan(inspect.NetworkSettings)}
}

// createFromSpec creates a container named name from spec and connects its
// extra networks. A failed network connect is logged, not fatal: the
// container can run without its secondary networks.
func (a *Agent) createFromSpec(ctx context.Context, name string, spec recreateSpec) (string, error) {
	id, err := a.docker.CreateContainer(ctx, name, spec.cfg, spec.hostCfg, spec.nets.primary)
	if err != nil {
		return "", err
	}
	for _, netName := range spec.nets.extras {
		if err := a.docker.NetworkConnect(ctx, netName, id); err != nil {
			a.log.Error("extra network connect failed", "container", name, "network", netName, "error", err)
		}
	}
	return id, nil
}

// parkedContainer is an old container moved aside during a recreate.
type parkedContainer struct {
	id, name    string
	inspect     *container.InspectResponse
	targetImage string // image the failed replacement was built from
	wasRunning  bool
}

func isRunning(inspect *container.InspectResponse) bool {
	return inspect.State != nil && inspect.State.Running
}

func autoRemoves(inspect *container.InspectResponse) bool {
	return inspect.HostConfig != nil && inspect.HostConfig.AutoRemove
}

// restoreParked undoes a failed recreate: it removes the half-made
// replacement (if any), gives the old container its name back and starts
// it again if it was running. A --rm container is deleted by the daemon
// when it stops, so it is created again from its inspected config on its
// old image instead. Errors are logged: the caller is already returning
// the failure that got us here. The rollback runs to the end even if ctx
// has been cancelled.
func (a *Agent) restoreParked(ctx context.Context, old parkedContainer, newID string) {
	ctx = context.WithoutCancel(ctx)

	if newID != "" {
		if err := a.docker.RemoveContainer(ctx, newID); err != nil {
			a.log.Error("rollback: failed to remove the replacement container", "container", old.name, "id", truncateID(newID), "error", err)
		}
	}

	if err := a.docker.RenameContainer(ctx, old.id, old.name); err != nil {
		if !autoRemoves(old.inspect) {
			a.log.Error("rollback: failed to give the old container its name back; it is still on the host",
				"container", old.name, "parked_as", parkedName(old.name), "error", err)
			return
		}
		a.recreateRemoved(ctx, old)
		return
	}

	if !old.wasRunning {
		return
	}
	if err := a.docker.StartContainer(ctx, old.id); err != nil {
		a.log.Error("rollback: failed to start the old container", "container", old.name, "error", err)
	}
}

// recreateRemoved brings back a --rm container that the daemon deleted when
// it was stopped. For a same-tag update the tag now names the new image, so
// the old image is used by ID.
func (a *Agent) recreateRemoved(ctx context.Context, old parkedContainer) {
	image := old.inspect.Config.Image
	if image == old.targetImage && old.inspect.Image != "" {
		image = old.inspect.Image
	}
	id, err := a.createFromSpec(ctx, old.name, buildRecreateSpec(old.inspect, image))
	if err != nil {
		a.log.Error("rollback: failed to recreate the removed --rm container", "container", old.name, "image", image, "error", err)
		return
	}
	if !old.wasRunning {
		return
	}
	if err := a.docker.StartContainer(ctx, id); err != nil {
		a.log.Error("rollback: failed to start the recreated --rm container", "container", old.name, "error", err)
	}
}

// networkPlan separates a container's networks into a primary (for
// CreateContainer) and extras (for subsequent NetworkConnect calls).
// Docker API 1.44+ rejects creation with multiple endpoints, so only
// one can go in the NetworkingConfig. Bridge is skipped entirely as
// Docker attaches it automatically. Only user-specified fields (IPAM,
// aliases, driver opts) are copied; runtime fields (Gateway, IPAddress)
// would conflict on the new container.
type networkPlan struct {
	primary *network.NetworkingConfig
	extras  []string // network names to connect after create
}

func buildNetworkPlan(ns *container.NetworkSettings) networkPlan {
	var plan networkPlan
	if ns == nil {
		return plan
	}

	var primaryName string
	for netName := range ns.Networks {
		if netName == "bridge" {
			continue
		}
		if primaryName == "" {
			primaryName = netName
		} else {
			plan.extras = append(plan.extras, netName)
		}
	}

	if primaryName != "" {
		ep := ns.Networks[primaryName]
		plan.primary = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				primaryName: {
					IPAMConfig: ep.IPAMConfig,
					Aliases:    ep.Aliases,
					DriverOpts: ep.DriverOpts,
					NetworkID:  ep.NetworkID,
					MacAddress: ep.MacAddress,
				},
			},
		}
	}

	return plan
}

// configFromInspect extracts container creation parameters from an
// InspectResponse, replacing the image with targetImage. This preserves
// env vars, volumes, ports, and all other configuration from the original
// container. Network config is handled separately via buildNetworkPlan.
func configFromInspect(inspect *container.InspectResponse, targetImage string) (*container.Config, *container.HostConfig) {
	cfgCopy := *inspect.Config
	cfgCopy.Image = targetImage
	hostCfgCopy := *inspect.HostConfig
	return &cfgCopy, &hostCfgCopy
}

// selfUpdateContainer uses the rename-before-replace pattern to update the
// agent's own container without killing itself mid-update. The flow:
//  1. Inspect to capture full config
//  2. Pull the new image
//  3. Rename self out of the way (container keeps running)
//  4. Create new container with original name + new image
//  5. Connect extra networks beyond the primary
//  6. Start the new container
//  7. Old agent exits naturally (the new container takes over)
//
// On failure at step 4 or 6, the old container is renamed back.
// selfUpdateResult extends the standard update return values with the old
// container ID, so the caller can stop/remove it after reporting success.
type selfUpdateResult struct {
	oldImage, oldDigest, newDigest string
	newImage                       string
	oldContainerID                 string
}

func (a *Agent) selfUpdateContainer(ctx context.Context, name, targetImage string) (selfUpdateResult, error) {
	var r selfUpdateResult

	cID, err := a.findContainerID(ctx, name)
	if err != nil {
		return r, fmt.Errorf("find container %s: %w", name, err)
	}
	r.oldContainerID = cID

	inspect, err := a.docker.InspectContainer(ctx, cID)
	if err != nil {
		return r, fmt.Errorf("inspect %s: %w", name, err)
	}

	targetImage = targetOrCurrent(targetImage, &inspect)
	r.newImage = targetImage
	r.oldImage = inspect.Config.Image
	r.oldDigest, _ = a.docker.ImageDigest(ctx, r.oldImage)

	// Pull first, before any destructive changes. If pull fails but the
	// image is already available locally, continue (supports local/dev images).
	if err := a.docker.PullImage(ctx, targetImage); err != nil {
		if _, localErr := a.docker.ImageDigest(ctx, targetImage); localErr != nil {
			return r, fmt.Errorf("pull %s: %w", targetImage, err)
		}
		a.log.Warn("pull failed but image available locally", "image", targetImage, "pull_error", err)
	}
	r.newDigest, _ = a.docker.ImageDigest(ctx, targetImage)

	// Rename self out of the way. The container keeps running.
	oldName := fmt.Sprintf("%s-old-%d", name, time.Now().Unix())
	a.log.Info("self-update: renaming self", "from", name, "to", oldName)
	if err := a.docker.RenameContainer(ctx, cID, oldName); err != nil {
		return r, fmt.Errorf("rename self: %w", err)
	}

	// Create new container with the original name, then connect extra
	// networks beyond the primary.
	a.log.Info("self-update: creating replacement", "name", name, "image", targetImage)
	newID, err := a.createFromSpec(ctx, name, buildRecreateSpec(&inspect, targetImage))
	if err != nil {
		a.log.Error("self-update: create failed, rolling back rename", "error", err)
		_ = a.docker.RenameContainer(ctx, cID, name)
		return r, fmt.Errorf("create replacement: %w", err)
	}

	// Start the replacement.
	a.log.Info("self-update: starting replacement", "id", truncateID(newID))
	if err := a.docker.StartContainer(ctx, newID); err != nil {
		a.log.Error("self-update: start failed, rolling back", "error", err)
		_ = a.docker.RemoveContainer(ctx, newID)
		_ = a.docker.RenameContainer(ctx, cID, name)
		return r, fmt.Errorf("start replacement: %w", err)
	}

	a.log.Info("self-update: complete, old container can be stopped")
	return r, nil
}

// isSelfContainer checks if the named container has the sentinel.self=true
// label, meaning it's this agent's own container.
func (a *Agent) isSelfContainer(ctx context.Context, name string) bool {
	cID, err := a.findContainerID(ctx, name)
	if err != nil {
		return false
	}
	inspect, err := a.docker.InspectContainer(ctx, cID)
	if err != nil {
		return false
	}
	if inspect.Config == nil {
		return false
	}
	return inspect.Config.Labels["sentinel.self"] == "true"
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

// truncateID returns the first 12 characters of a container ID for
// logging. Handles short/empty IDs without panicking.
func truncateID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
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
