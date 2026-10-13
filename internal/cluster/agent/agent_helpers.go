package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/selfupdate"
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
	// warning is set when the update worked but left something behind.
	warning string
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

// parkedSuffix marks the name a container is moved to while its
// replacement is built (parkedName).
const parkedSuffix = "-sentinel-old"

func parkedName(name string) string {
	return name + parkedSuffix
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
// its name back and is started again. The running state is the one the
// container had once parked: a container that was not running then comes
// back stopped, updated or not. An empty targetImage re-pulls the
// container's current image.
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

	spec := buildRecreateSpec(&inspect, docker.RecreateOptions{
		Image:                 targetImage,
		OldImage:              a.imageDefaults(ctx, inspect.Image, name),
		FreshAnonymousVolumes: a.removeVolumes(&inspect),
	})

	newID, started, warning, err := a.swapIn(ctx, name, cID, &inspect, spec, targetImage, true)
	r.warning = warning
	if err != nil {
		return r, err
	}

	// Containers sharing this container's network namespace follow it to
	// the replacement (they cannot join one that is not running).
	if started {
		a.afterProviderChange(ctx, name, newID, cID)
	}
	return r, nil
}

// swapIn replaces the container cID, named name and described by inspect,
// with one created from spec, so that the host is never without it: steps
// 2 to 5 of recreateContainer. On failure the old container is restored;
// repairConsumers makes a restore that stopped or replaced it also repair
// the containers in its network namespace. It returns the replacement's ID,
// whether it was started (it is only if the old one was running), and a
// warning when the update worked but left something behind.
func (a *Agent) swapIn(ctx context.Context, name, cID string, inspect *container.InspectResponse, spec recreateSpec, targetImage string, repairConsumers bool) (newID string, started bool, warning string, err error) {
	parked := parkedName(name)
	if id, err := a.findContainerID(ctx, parked); err == nil {
		if err := a.clearLeftover(ctx, name, parked, id, inspect); err != nil {
			return "", false, "", err
		}
	}
	if err := a.docker.RenameContainer(ctx, cID, parked); err != nil {
		return "", false, "", fmt.Errorf("rename %s to %s: %w", name, parked, err)
	}
	old := parkedContainer{id: cID, name: name, inspect: inspect, targetImage: targetImage,
		freshVolumes: spec.freshVolumes, repairConsumers: repairConsumers}

	// Take the running state now that the container is parked: it may have
	// been started or stopped since the first inspect, and from here on
	// anything acting on it by name reaches the replacement instead.
	parkedInspect, err := a.docker.InspectContainer(ctx, cID)
	if err != nil {
		old.wasRunning = isRunning(inspect)
		a.restoreParked(ctx, old, "")
		return "", false, "", fmt.Errorf("inspect %s after parking it: %w", name, err)
	}
	old.wasRunning = isRunning(&parkedInspect)

	newID, err = a.createFromSpec(ctx, name, spec)
	if err != nil {
		a.restoreParked(ctx, old, "")
		return "", false, "", fmt.Errorf("create %s: %w", name, err)
	}

	// Stop the old container whatever its state, so the remove below only
	// ever removes a container the agent stopped (for one that is already
	// stopped this changes nothing). The replacement needs its ports. 30s
	// timeout is generous but safe.
	if err := a.docker.StopContainer(ctx, cID, 30); err != nil {
		a.restoreParked(ctx, old, newID)
		return "", false, "", fmt.Errorf("stop %s: %w", name, err)
	}
	old.stopped = true

	if old.wasRunning {
		if err := a.docker.StartContainer(ctx, newID); err != nil {
			a.restoreParked(ctx, old, newID)
			return "", false, "", fmt.Errorf("start %s: %w", name, err)
		}
		started = true
	}

	// A --rm container was already deleted by the daemon when it stopped.
	// The old container's anonymous volumes go with it only if the
	// replacement started on fresh ones; otherwise the replacement uses them.
	if err := a.removeContainer(ctx, cID, spec.freshVolumes); err != nil && !autoRemoves(inspect) {
		a.log.Warn("container updated, but the old one could not be removed",
			"container", name, "old_container", parked, "error", err)
		warning = fmt.Sprintf("updated, but the old container %s could not be removed: %v", parked, err)
	}
	return newID, started, warning, nil
}

// afterProviderChange repairs the running containers that share the
// network namespace (network_mode container:X) of the container now
// running as name (currentID), which replaced or restarted previousIDs.
// The rules are the local engine's (Updater.repairNetworkNamespace): a
// consumer that refers to the provider by name or by its current ID is
// restarted, which rejoins it; one that refers to a previous ID is left
// with only loopback and cannot even restart, so it is recreated with
// container:<currentID>. Containers parked by an update are skipped, and
// so is a consumer whose own update is running.
func (a *Agent) afterProviderChange(ctx context.Context, name, currentID string, previousIDs ...string) {
	ctx = context.WithoutCancel(ctx)
	var previous []string
	for _, id := range previousIDs {
		if id != "" && id != currentID {
			previous = append(previous, id)
		}
	}

	running, err := a.docker.ListContainers(ctx)
	if err != nil {
		a.log.Warn("namespace check: list failed", "provider", name, "error", err)
		return
	}
	for i := range running {
		c := &running[i]
		cName := containerInfoFromSummary(c).GetName()
		if cName == name || strings.HasSuffix(cName, parkedSuffix) {
			continue
		}
		dep, err := a.docker.InspectContainer(ctx, c.ID)
		if err != nil || dep.HostConfig == nil || !dep.HostConfig.NetworkMode.IsContainer() {
			continue
		}
		ref := dep.HostConfig.NetworkMode.ConnectedContainer()
		switch {
		case ref == name || sameContainerID(ref, currentID):
			if a.isUpdating(cName) {
				a.log.Info("network dependent is being updated, not restarting it", "dependent", cName, "provider", name)
				continue
			}
			a.log.Info("restarting network dependent", "dependent", cName, "provider", name)
			if err := a.docker.RestartContainer(ctx, c.ID); err != nil {
				a.log.Warn("failed to restart network dependent", "dependent", cName, "error", err)
			}
		case slices.ContainsFunc(previous, func(old string) bool { return sameContainerID(ref, old) }):
			a.recreateNamespaceConsumer(ctx, c.ID, cName, dep, currentID, name)
		}
	}
}

// recreateNamespaceConsumer recreates a container that shared the network
// namespace of a provider that has since been replaced, so that it joins
// the provider's new container. It goes through the same swap as an
// update, so a failure leaves the consumer as it was.
func (a *Agent) recreateNamespaceConsumer(ctx context.Context, consumerID, consumerName string, consumer container.InspectResponse, providerID, providerName string) {
	if !a.beginUpdate(consumerName) {
		a.log.Warn("network dependent is being updated, not recreating it", "dependent", consumerName, "provider", providerName)
		return
	}
	defer a.endUpdate(consumerName)

	spec := buildRecreateSpec(&consumer, docker.RecreateOptions{})
	spec.hostCfg.NetworkMode = container.NetworkMode("container:" + providerID)
	a.log.Info("recreating network dependent on the updated provider", "dependent", consumerName, "provider", providerName)
	var image string
	if consumer.Config != nil {
		image = consumer.Config.Image
	}
	if _, _, _, err := a.swapIn(ctx, consumerName, consumerID, &consumer, spec, image, false); err != nil {
		a.log.Error("failed to recreate network dependent", "dependent", consumerName, "error", err)
	}
}

// sameContainerID reports whether two container IDs name the same
// container, allowing for one of them to be a short (12+ digit) prefix.
func sameContainerID(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) < 12 || len(b) < 12 {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// clearLeftover deals with a container already under the parked name when
// an update of name starts. It is removed only when it is stopped and the
// container under the real name (current) is running or has run: then the
// leftover is the old copy from an update interrupted after its
// replacement took over. Otherwise the leftover may be the working
// original, so it is left alone and the update refused.
func (a *Agent) clearLeftover(ctx context.Context, name, parked, id string, current *container.InspectResponse) error {
	inspect, err := a.docker.InspectContainer(ctx, id)
	if err == nil && !isRunning(&inspect) && (isRunning(current) || hasRun(current)) {
		if err := a.docker.RemoveContainer(ctx, id); err == nil {
			a.log.Info("removed a stopped container left by an interrupted update", "container", name, "leftover", parked, "id", truncateID(id))
			return nil
		}
	}
	return fmt.Errorf("container %s (%s) is left over from an interrupted update and is running or cannot be removed; remove it, or rename it to %s if %s is missing, then retry",
		parked, truncateID(id), name, name)
}

// recreateSpec is everything needed to create a container that replaces
// an existing one. It comes from docker.RecreateConfig, which is also what
// the local engine uses, so a recreated container looks the same whether
// the hub or an agent made it: the new image's defaults rather than the old
// one's, the old container's anonymous volumes, every network's settings,
// no hostname or alias taken from the old container's ID, and the fields a
// container:X network mode refuses left out. The agent adds only its own
// park, start and restore steps around it.
type recreateSpec struct {
	cfg     *container.Config
	hostCfg *container.HostConfig
	netCfg  *network.NetworkingConfig
	// freshVolumes: the replacement starts on new anonymous volumes, so
	// the old container's go with the old container (and a rolled-back
	// replacement's go with it).
	freshVolumes bool
}

func buildRecreateSpec(inspect *container.InspectResponse, opts docker.RecreateOptions) recreateSpec {
	rc := docker.RecreateConfig(*inspect, opts)
	return recreateSpec{cfg: rc.Config, hostCfg: rc.HostConfig, netCfg: rc.NetworkingConfig, freshVolumes: opts.FreshAnonymousVolumes}
}

// createFromSpec creates a container named name from spec, attached to all
// of its networks.
func (a *Agent) createFromSpec(ctx context.Context, name string, spec recreateSpec) (string, error) {
	return a.docker.CreateContainer(ctx, name, spec.cfg, spec.hostCfg, spec.netCfg)
}

// imageDefaults returns the config of the image a container was created
// from (by image ID), so the recreate can leave out what came from it. It
// returns nil, which keeps the old config whole, when the image cannot be
// read.
func (a *Agent) imageDefaults(ctx context.Context, imageID, name string) *container.Config {
	reader, ok := a.docker.(docker.ImageConfigReader)
	if !ok || imageID == "" {
		return nil
	}
	cfg, err := reader.ImageConfig(ctx, imageID)
	if err != nil {
		a.log.Warn("could not read the old image's defaults, keeping the old config as it is",
			"container", name, "image", imageID, "error", err)
		return nil
	}
	return cfg
}

// removeVolumes reports whether a container is updated onto fresh anonymous
// volumes, as `docker compose up -V` does: its sentinel.remove-volumes
// label, or this agent's own remove_volumes setting. Otherwise the
// replacement keeps the old ones.
func (a *Agent) removeVolumes(inspect *container.InspectResponse) bool {
	return (inspect.Config != nil && docker.ContainerRemoveVolumes(inspect.Config.Labels)) || a.cfg.RemoveVolumes
}

// removeContainer force-removes a container, with its anonymous volumes
// only when withVolumes is set.
func (a *Agent) removeContainer(ctx context.Context, id string, withVolumes bool) error {
	if withVolumes {
		return a.docker.RemoveContainerWithVolumes(ctx, id)
	}
	return a.docker.RemoveContainer(ctx, id)
}

// parkedContainer is an old container moved aside during a recreate.
type parkedContainer struct {
	id, name     string
	inspect      *container.InspectResponse
	targetImage  string // image the failed replacement was built from
	wasRunning   bool   // running when it was parked
	stopped      bool   // the agent has stopped it
	freshVolumes bool   // the replacement was given new anonymous volumes
	// repairConsumers: once restored, repair the containers in its
	// network namespace (afterProviderChange).
	repairConsumers bool
}

func isRunning(inspect *container.InspectResponse) bool {
	return inspect.State != nil && inspect.State.Running
}

// hasRun reports whether the container has been started at some point.
func hasRun(inspect *container.InspectResponse) bool {
	if inspect.State == nil || inspect.State.StartedAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, inspect.State.StartedAt)
	return err == nil && !t.IsZero()
}

func autoRemoves(inspect *container.InspectResponse) bool {
	return inspect.HostConfig != nil && inspect.HostConfig.AutoRemove
}

// restoreParked undoes a failed recreate: it removes the half-made
// replacement (if any), gives the old container its name back and starts
// it again if it was running. A --rm container the agent stopped is being
// deleted by the daemon, so it is created again from its inspected config
// on its old image instead. A replacement that cannot be removed is
// renamed aside so the old container can take its name back. Errors are
// logged: the caller is already returning the failure that got us here.
// The rollback runs to the end even if ctx has been cancelled.
func (a *Agent) restoreParked(ctx context.Context, old parkedContainer, newID string) {
	ctx = context.WithoutCancel(ctx)

	if newID != "" {
		// Its anonymous volumes are the old container's unless it was given
		// fresh ones: keep those for the restore.
		if err := a.removeContainer(ctx, newID, old.freshVolumes); err != nil {
			// It still holds the real name: move it aside so the old
			// container can have the name back.
			a.log.Error("rollback: failed to remove the replacement container, moving it aside", "container", old.name, "id", truncateID(newID), "error", err)
			aside := old.name + "-sentinel-failed"
			if err := a.docker.RenameContainer(ctx, newID, aside); err != nil {
				aside = fmt.Sprintf("%s-sentinel-failed-%d", old.name, time.Now().Unix())
				if err := a.docker.RenameContainer(ctx, newID, aside); err != nil {
					a.log.Error("rollback: failed to move the replacement aside", "container", old.name, "error", err)
				}
			}
		}
	}

	// The daemon is deleting a --rm container the agent stopped, even if it
	// is still visible: renaming it back would lose it. Make sure it is
	// gone and create it again.
	if autoRemoves(old.inspect) && old.stopped {
		if err := a.docker.RemoveContainer(ctx, old.id); err != nil {
			a.log.Debug("rollback: --rm container already being removed", "container", old.name, "error", err)
		}
		a.recreateRemoved(ctx, old)
		return
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
		return
	}
	// Stopping it cut off the containers in its network namespace.
	if old.repairConsumers && old.stopped {
		a.afterProviderChange(ctx, old.name, old.id)
	}
}

// recreateRemoved brings back a --rm container that the daemon deleted when
// it was stopped, as it was: nothing is left out as an image default, and
// it gets its own anonymous volumes back. For a same-tag update the tag now
// names the new image, so the old image is used by ID.
func (a *Agent) recreateRemoved(ctx context.Context, old parkedContainer) {
	image := old.inspect.Config.Image
	if image == old.targetImage && old.inspect.Image != "" {
		image = old.inspect.Image
	}
	id, err := a.createFromSpec(ctx, old.name, buildRecreateSpec(old.inspect, docker.RecreateOptions{Image: image}))
	if err != nil {
		a.log.Error("rollback: failed to recreate the removed --rm container", "container", old.name, "image", image, "error", err)
		return
	}
	if !old.wasRunning {
		return
	}
	if err := a.docker.StartContainer(ctx, id); err != nil {
		a.log.Error("rollback: failed to start the recreated --rm container", "container", old.name, "error", err)
		return
	}
	if old.repairConsumers {
		a.afterProviderChange(ctx, old.name, id, old.id)
	}
}

// selfUpdateResult extends the standard update return values with what the
// caller needs to finish a self-update after reporting its result.
type selfUpdateResult struct {
	oldImage, oldDigest, newDigest string
	newImage                       string
	// oldContainerID is set when the replacement was started in-process: the
	// caller stops the old container once the result is sent.
	oldContainerID string
	// handover is set when a helper container does the swap: the caller sends
	// the result, then calls handover.Wait. The helper stops this agent a few
	// seconds after it was started.
	handover *selfupdate.Handover
}

// selfUpdateContainer updates the agent's own container without killing
// itself mid-update. The agent cannot free its own published ports or
// database lock before the new one starts, so with the Docker socket mounted
// it sets the replacement up and hands the swap to a one-shot helper
// container (package selfupdate): the helper stops this container, starts the
// new one, waits for it to settle and puts this one back if it does not.
// Without the socket (a TCP Docker host) the replacement is started in-process
// while this container keeps running, and the caller stops it afterwards;
// that fails with published host ports.
//
// Leftover "-old-" containers and helpers are removed at agent start.
func (a *Agent) selfUpdateContainer(ctx context.Context, name, targetImage string) (selfUpdateResult, error) {
	var r selfUpdateResult

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

	defaults := selfupdate.ImageDefaults(ctx, a.docker, inspect.Image, a.log)

	if !strings.HasPrefix(a.cfg.DockerSock, "tcp://") && !strings.HasPrefix(a.cfg.DockerSock, "tcps://") {
		h, err := selfupdate.Begin(ctx, selfupdate.Params{
			Docker: a.docker, Log: a.log, Inspect: inspect, Name: name, Image: targetImage,
			OldImageDefaults: defaults, DataDir: a.cfg.DataDir, Socket: a.cfg.DockerSock,
			// Long enough for the result to reach the hub before this agent stops.
			Delay: 5 * time.Second,
		})
		switch {
		case err == nil:
			a.log.Info("self-update: handed over to the helper container", "old", h.OldName())
			r.handover = h
			return r, nil
		case !errors.Is(err, selfupdate.ErrNoSocket):
			return r, err
		}
		a.log.Info("self-update: the Docker socket is not mounted here, replacing in-process")
	}

	r.oldContainerID = cID
	// Rename self out of the way. The container keeps running.
	oldName := fmt.Sprintf("%s-old-%d", name, time.Now().Unix())
	a.log.Info("self-update: renaming self", "from", name, "to", oldName)
	if err := a.docker.RenameContainer(ctx, cID, oldName); err != nil {
		return r, fmt.Errorf("rename self: %w", err)
	}

	// The agent's own data directory may be an anonymous volume: it is
	// always carried, never replaced by a fresh one.
	a.log.Info("self-update: creating replacement", "name", name, "image", targetImage)
	rc := selfupdate.BuildReplacement(inspect, targetImage, defaults)
	newID, err := a.docker.CreateContainer(ctx, name, rc.Config, rc.HostConfig, rc.NetworkingConfig)
	if err != nil {
		a.log.Error("self-update: create failed, rolling back rename", "error", err)
		_ = a.docker.RenameContainer(ctx, cID, name)
		return r, fmt.Errorf("create replacement: %w", err)
	}

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
		// A container parked by an update that is running now is part of
		// that update, not one of the host's own. One with no update
		// running is stranded and may be the only copy, so it is reported.
		if a.parkedByRunningUpdate(ci.Name) {
			continue
		}

		// Populate the image digest so the server can compare against
		// the registry without needing the image locally.
		if digest, err := a.docker.ImageDigest(ctx, summaries[i].Image); err == nil {
			ci.ImageDigest = digest
		}

		out = append(out, ci)
	}
	return out, nil
}

// parkedByRunningUpdate reports whether name is the parked copy of a
// container whose update is in progress.
func (a *Agent) parkedByRunningUpdate(name string) bool {
	base, ok := strings.CutSuffix(name, parkedSuffix)
	if !ok {
		return false
	}
	a.updatingMu.Lock()
	defer a.updatingMu.Unlock()
	return a.updating[base]
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
