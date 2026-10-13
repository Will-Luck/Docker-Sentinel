package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker/imageconfig"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
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

// heartbeatInterval is how often the agent sends a heartbeat, and
// listRefreshEvery how many heartbeats apart it also sends its container
// list (5 minutes), so the hub's view of the host does not wait for the
// next scan. Variables so tests can shorten them.
var (
	heartbeatInterval = 30 * time.Second
	listRefreshEvery  = 10
)

// heartbeatLoop sends periodic heartbeats to the server. Returns on
// stream error or context cancellation.
func (a *Agent) heartbeatLoop(ctx context.Context, stream proto.AgentService_ChannelClient) error {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	beats := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			beats++
			if beats%listRefreshEvery == 0 {
				if err := a.handleListContainers(ctx, stream, ""); err != nil && ctx.Err() == nil {
					a.log.Warn("could not send the container list", "error", err)
				}
			}
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
// configuration on targetImage, and never leaves the host without it. It
// pulls the image and builds the new configuration first (nothing is
// touched yet), then hands the replacement to the hub's journalled swap
// (engine.HostSwaps): the old container is renamed aside, the replacement
// created, the old one stopped and the replacement started, then the old
// one removed. Every step is journalled in the agent's database first, so
// if the agent is killed part way, it finishes or undoes the update when it
// starts again. On failure the old container is put back (or rebuilt, for a
// --rm container the daemon deleted). The replacement is started only if
// the old container was running: a stopped container comes back stopped,
// updated or not. An empty targetImage re-pulls the container's current
// image.
func (a *Agent) recreateContainer(ctx context.Context, name, targetImage string) (recreateResult, error) {
	return a.recreate(ctx, name, targetImage, updateOptions{})
}

// updateOptions is what the hub sends with an update besides its target.
type updateOptions struct {
	// previous are the images the container ran before its current one.
	previous []*proto.PreviousImage
	// hooks are the hooks the hub stores for the container; skipHooks
	// runs none.
	hooks     []*proto.HookSpec
	skipHooks bool
	// registryAuth is the X-Registry-Auth value for the target image's
	// registry, also used to fetch earlier images' configs.
	registryAuth string
}

// updateOptionsFrom reads the options of an update request.
func updateOptionsFrom(req *proto.UpdateContainerRequest) updateOptions {
	return updateOptions{previous: req.GetPreviousImages(), hooks: req.GetHooks(), skipHooks: req.GetSkipHooks(),
		registryAuth: req.GetRegistryAuth()}
}

// errSkippedByHook ends an update a pre-update hook asked to skip (exit
// 75): nothing was changed.
var errSkippedByHook = errors.New("a pre-update hook asked to skip the update (exit 75)")

// hookRunner returns the runner of the lifecycle hooks of one update, or
// nil when none run (hooks are off, or the hub asked to skip them). The
// hooks the hub sent win over the container's sentinel.hook.* labels,
// phase by phase, as the hub's stored hooks do on the hub.
func (a *Agent) hookRunner(name string, opts updateOptions) *hooks.Runner {
	if opts.skipHooks || !a.settings.HooksEnabled() {
		return nil
	}
	sent := make(sentHooks, 0, len(opts.hooks))
	for _, h := range opts.hooks {
		sent = append(sent, hooks.Hook{ContainerName: name, Phase: h.GetPhase(), Command: h.GetCommand(), Timeout: int(h.GetTimeoutSeconds())})
	}
	return hooks.NewRunner(a.docker, sent, a.log)
}

// sentHooks is the hook store of one update: the hooks the hub sent.
type sentHooks []hooks.Hook

func (s sentHooks) ListHooks(string) ([]hooks.Hook, error) { return s, nil }
func (sentHooks) SaveHook(hooks.Hook) error {
	return errors.New("the hooks the hub sent are read-only")
}
func (sentHooks) DeleteHook(string, string) error {
	return errors.New("the hooks the hub sent are read-only")
}

// recreate is recreateContainer with the options the hub sent.
func (a *Agent) recreate(ctx context.Context, name, targetImage string, opts updateOptions) (recreateResult, error) {
	var r recreateResult
	if a.swaps == nil {
		return r, errors.New("this agent has no database for its update journal, so it does not update containers")
	}
	// An earlier update that did not finish holds the only way back to its
	// old container: change nothing until its recovery has resolved it.
	if err := a.swaps.Pending(name, fmt.Sprintf("DELETE /api/swaps/%s?host=%s on the server", name, a.hostID)); err != nil {
		return r, err
	}

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

	// Pre-update hooks run in the old container before anything changes:
	// exit 75 skips the update, any other failure aborts it.
	runner := a.hookRunner(name, opts)
	if runner != nil {
		if err := runner.RunPreUpdate(ctx, cID, name, inspect.Config.Labels); err != nil {
			if errors.Is(err, hooks.ErrSkipUpdate) {
				a.log.Info("pre-update hook requested skip", "name", name)
				return r, errSkippedByHook
			}
			return r, fmt.Errorf("pre-update hook for %s: %w", name, err)
		}
	}

	a.swaps.BackupImage(ctx, name, r.oldImage)

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

	// Inspect again now the pull is done: the container may have been
	// started or stopped meanwhile, and the swap keeps the state it has now.
	inspect, err = a.docker.InspectContainer(ctx, cID)
	if err != nil {
		return r, fmt.Errorf("inspect %s: %w", name, err)
	}

	// A container under the set-aside name that no journal record explains
	// was left by an agent from before the journal.
	parked := parkedName(name)
	if id, err := a.findContainerID(ctx, parked); err == nil {
		if err := a.clearLeftover(ctx, name, parked, id, &inspect); err != nil {
			return r, err
		}
	}

	removeVolumes := a.removeVolumes(&inspect)
	rc := docker.RecreateConfig(inspect, docker.RecreateOptions{
		Image:                 targetImage,
		OldImage:              a.imageDefaults(ctx, inspect.Image, name),
		InheritedImages:       a.previousImageConfigs(ctx, name, targetImage, opts.registryAuth, opts.previous),
		FreshAnonymousVolumes: removeVolumes,
	})
	newID, warning, err := a.swaps.Update(ctx, engine.HostUpdate{
		Name: name, OldID: cID, Old: inspect, Recreate: rc, RemoveVolumes: removeVolumes,
	})
	r.warning = warning
	if err != nil {
		return r, err
	}

	// Post-update hooks run in the new container, with its own labels; a
	// failure is reported but undoes nothing.
	if runner != nil {
		var labels map[string]string
		if created, err := a.docker.InspectContainer(ctx, newID); err == nil && created.Config != nil {
			labels = created.Config.Labels
		}
		if err := runner.RunPostUpdate(ctx, newID, name, labels); err != nil {
			a.log.Warn("post-update hook failed", "name", name, "error", err)
			r.warning = joinWarnings(r.warning, fmt.Sprintf("post-update hook: %v", err))
		}
	}

	a.swaps.CleanupOldImage(ctx, inspect.Image, name)
	return r, nil
}

// joinWarnings joins two warnings, either of which may be empty.
func joinWarnings(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

// clearLeftover deals with a container already under the set-aside name,
// that no journal record explains, when an update of name starts: agents
// from before the journal left one when they were interrupted. It is
// removed only when it is stopped and the container under the real name
// (current) is running or has run: then the leftover is the old copy from
// an update interrupted after its replacement took over. Otherwise the
// leftover may be the working original, so it is left alone and the update
// refused.
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

// previousImageConfigs returns the configs of the images a container ran
// before its current one (prev, from the hub's history): read on the host
// when the image is still there, else fetched from its registry with the
// credentials the hub sent with the update (registryAuth, for the target
// image's registry). A container an older Sentinel updated kept the config
// of the first image it ran, so the recreate leaves out what came from
// these too. One that cannot be found is skipped, and what came from it is
// kept, as before: a lookup never fails the update.
func (a *Agent) previousImageConfigs(ctx context.Context, name, targetImage, registryAuth string, prev []*proto.PreviousImage) []*container.Config {
	if len(prev) == 0 {
		return nil
	}
	local, _ := a.docker.(docker.ImageConfigReader)
	creds := credentialsFromAuth(registryAuth, targetImage)
	newSource := a.imageConfigSource
	if newSource == nil {
		newSource = func(local docker.ImageConfigReader, creds func() []registry.RegistryCredential) imageConfigFinder {
			return imageconfig.New(local, creds)
		}
	}
	src := newSource(local, func() []registry.RegistryCredential { return creds })

	var out []*container.Config
	for _, p := range prev {
		if p.GetDigest() == "" {
			continue
		}
		fetchCtx, cancel := context.WithTimeout(ctx, previousImageTimeout)
		cfg, err := src.Config(fetchCtx, imageconfig.Image{Ref: p.GetRef(), Digest: p.GetDigest()})
		cancel()
		if err != nil {
			a.log.Debug("could not read an image the container ran before; keeping what it may have inherited from it",
				"container", name, "image", p.GetRef(), "digest", p.GetDigest(), "error", err)
			continue
		}
		out = append(out, cfg)
	}
	return out
}

// previousImageTimeout bounds the lookup of one earlier image's config.
const previousImageTimeout = 30 * time.Second

// credentialsFromAuth turns the X-Registry-Auth value the hub sent for an
// update into the registry credential of the target image's registry, for
// the registry lookups of the update. None when the hub sent none.
func credentialsFromAuth(encoded, targetImage string) []registry.RegistryCredential {
	if encoded == "" || targetImage == "" {
		return nil
	}
	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		if raw, err = base64.StdEncoding.DecodeString(encoded); err != nil {
			return nil
		}
	}
	var auth struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.Unmarshal(raw, &auth) != nil || auth.Username == "" {
		return nil
	}
	return []registry.RegistryCredential{{Registry: registry.RegistryHost(targetImage), Username: auth.Username, Secret: auth.Password}}
}

// removeVolumes reports whether a container is updated onto fresh anonymous
// volumes, as `docker compose up -V` does: its sentinel.remove-volumes
// label, or this agent's remove_volumes setting. Otherwise the replacement
// keeps the old ones.
func (a *Agent) removeVolumes(inspect *container.InspectResponse) bool {
	return (inspect.Config != nil && docker.ContainerRemoveVolumes(inspect.Config.Labels)) || a.settings.RemoveVolumes()
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

// ownContainerName returns the name of the container this agent runs in, found
// by the container ID. It fails, and nothing is guessed, when the ID cannot be
// determined or is not among this host's containers.
func (a *Agent) ownContainerName(ctx context.Context) (string, error) {
	id := selfupdate.SelfID
	if a.selfID != nil {
		id = a.selfID
	}
	containers, err := a.docker.ListAllContainers(ctx)
	if err != nil {
		return "", fmt.Errorf("list containers: %w", err)
	}
	self, err := selfupdate.ResolveSelf(containers, id())
	if err != nil {
		return "", err
	}
	if len(self.Names) == 0 {
		return "", selfupdate.ErrSelfUnknown
	}
	return strings.TrimPrefix(self.Names[0], "/"), nil
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
		// A container a journalled swap has set aside is part of that
		// swap (running, or waiting for recovery), not one of the host's
		// own. One that no journal record explains is reported.
		if a.swaps != nil && a.swaps.SetAside(ci.Name) {
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
