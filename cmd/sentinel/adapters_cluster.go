package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	clusterserver "github.com/Will-Luck/Docker-Sentinel/internal/cluster/server"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/npm"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/web"
	"google.golang.org/protobuf/types/known/durationpb"
)

// clusterAdapter bridges cluster/server.Server to web.ClusterProvider.
type clusterAdapter struct {
	srv   *clusterserver.Server
	store *store.Store
	// updater sends the notifications of the remote updates the dashboard
	// and the API start (nil: none).
	updater *engine.Updater
	log     *slog.Logger // nil: none
}

func (a *clusterAdapter) AllHosts() []web.ClusterHost {
	infos := a.srv.AllHosts()
	result := make([]web.ClusterHost, 0, len(infos))
	for _, h := range infos {
		// Use GetHost to get full HostState (includes ephemeral
		// fields like Connected and in-memory Containers).
		if hs, ok := a.srv.GetHost(h.ID); ok {
			result = append(result, web.ClusterHost{
				ID:            hs.Info.ID,
				Name:          hs.Info.Name,
				Address:       hs.Info.Address,
				State:         string(hs.Info.State),
				Connected:     hs.Connected,
				EnrolledAt:    hs.Info.EnrolledAt,
				LastSeen:      hs.Info.LastSeen,
				AgentVersion:  hs.Info.AgentVersion,
				Containers:    len(hs.Containers),
				DisconnectAt:  hs.DisconnectAt,
				DisconnectErr: hs.DisconnectErr,
				DisconnectCat: hs.DisconnectCat,
				EngineID:      hs.Info.EngineID,

				EngineSharedWith: a.hostNames(hs.EngineSharedWith),
			})
		}
	}
	return result
}

func (a *clusterAdapter) GetHost(id string) (web.ClusterHost, bool) {
	hs, ok := a.srv.GetHost(id)
	if !ok {
		return web.ClusterHost{}, false
	}
	return web.ClusterHost{
		ID:            hs.Info.ID,
		Name:          hs.Info.Name,
		Address:       hs.Info.Address,
		State:         string(hs.Info.State),
		Connected:     hs.Connected,
		EnrolledAt:    hs.Info.EnrolledAt,
		LastSeen:      hs.Info.LastSeen,
		AgentVersion:  hs.Info.AgentVersion,
		Containers:    len(hs.Containers),
		DisconnectAt:  hs.DisconnectAt,
		DisconnectErr: hs.DisconnectErr,
		DisconnectCat: hs.DisconnectCat,
		EngineID:      hs.Info.EngineID,

		EngineSharedWith: a.hostNames(hs.EngineSharedWith),
	}, true
}

func (a *clusterAdapter) AllHostContainers() []web.RemoteContainer {
	var result []web.RemoteContainer
	for _, info := range a.srv.AllHosts() {
		hs, ok := a.srv.GetHost(info.ID)
		if !ok {
			continue
		}
		for _, c := range hs.Containers {
			var ports []web.PortMapping
			for _, p := range c.Ports {
				ports = append(ports, web.PortMapping{
					HostIP:        p.HostIP,
					HostPort:      p.HostPort,
					ContainerPort: p.ContainerPort,
					Protocol:      p.Protocol,
				})
			}
			result = append(result, web.RemoteContainer{
				ID:          c.ID,
				Name:        c.Name,
				Image:       c.Image,
				ImageDigest: c.ImageDigest,
				State:       c.State,
				HostID:      info.ID,
				HostName:    info.Name,
				Labels:      c.Labels,
				Ports:       ports,
			})
		}
	}
	return result
}

func (a *clusterAdapter) ConnectedHosts() []string {
	return a.srv.ConnectedHosts()
}

func (a *clusterAdapter) GenerateEnrollToken() (string, string, error) {
	return a.srv.GenerateEnrollToken(24 * time.Hour)
}

func (a *clusterAdapter) RemoveHost(id string) error {
	return a.srv.RemoveHost(id)
}

func (a *clusterAdapter) RevokeHost(id string) error {
	return a.srv.RevokeHost(id)
}

func (a *clusterAdapter) PauseHost(id string) error {
	return a.srv.PauseHost(id)
}

func (a *clusterAdapter) ResumeHost(id string) error {
	return a.srv.ResumeHost(id)
}

func (a *clusterAdapter) DiscardRemoteSwap(ctx context.Context, hostID, name string) error {
	return a.srv.DiscardSwapSync(ctx, hostID, name)
}

func (a *clusterAdapter) UpdateRemoteContainer(ctx context.Context, hostID, containerName, targetImage, targetDigest string) (web.RemoteUpdated, error) {
	hostName, oldImage, changes := hostID, "", containerName
	var labels map[string]string
	if hs, ok := a.srv.GetHost(hostID); ok {
		hostName = hs.Info.Name
		for _, c := range hs.Containers {
			if c.Name == containerName {
				oldImage, labels = c.Image, c.Labels
			}
		}
		// An update of a Sentinel container changes the agent's own, which
		// may be another (the agent says which): the notifications name it.
		if own := hs.Info.SelfContainer; labels["sentinel.self"] == "true" && own != "" && own != containerName {
			changes, oldImage = own, ""
			for _, c := range hs.Containers {
				if c.Name == own {
					oldImage = c.Image
				}
			}
		}
	}
	// An update whose image cannot be verified at all is refused before
	// anything is sent, and, as a local one, without notifications (the
	// dispatch below returns the refusal).
	announce := a.updater != nil && !engine.IsUnverifiable(a.updater.RemoteRefusal(labels))
	if announce {
		a.updater.NotifyRemote(ctx, hostName, changes, oldImage, nil)
	}
	// The dashboard and the API record the history row themselves.
	ur, err := a.srv.UpdateContainerSync(clusterserver.WithCallerRecords(ctx), hostID, containerName, targetImage, targetDigest)
	if err == nil && ur.GetOutcome() == cluster.OutcomePending {
		// The agent's helper does its update of itself; the hub records and
		// notifies it when the outcome is known.
		return web.RemoteUpdated{ContainerName: ur.GetContainerName(), OldImage: ur.GetOldImage(), NewImage: ur.GetNewImage(), Pending: true}, nil
	}
	if announce {
		res := engine.RemoteUpdateResult{Outcome: "failed", NewImage: targetImage}
		if err != nil {
			res.Error = err.Error()
		} else {
			res = engine.RemoteUpdateResult{OldImage: ur.OldImage, NewImage: ur.NewImage, NewDigest: ur.NewDigest, Outcome: ur.Outcome, Error: ur.Error}
		}
		a.updater.NotifyRemote(context.WithoutCancel(ctx), hostName, changes, oldImage, &res)
	}
	if err != nil {
		return web.RemoteUpdated{}, err
	}
	changed := web.RemoteUpdated{
		ContainerName: ur.GetContainerName(), OldImage: ur.GetOldImage(), OldDigest: ur.GetOldDigest(),
		NewImage: ur.GetNewImage(), NewDigest: ur.GetNewDigest(),
	}
	return changed, remoteResultErr(ur, "update")
}

// remoteResultErr turns an agent's answer to an update or rollback (what)
// into the caller's error: nil for success, cluster.ErrUpdateSkipped when
// a pre-update hook asked to skip, else the agent's error.
func remoteResultErr(ur *proto.UpdateResult, what string) error {
	switch ur.Outcome {
	case "success":
		return nil
	case "skipped":
		return fmt.Errorf("%w (exit 75)", cluster.ErrUpdateSkipped)
	}
	if ur.Error != "" {
		return errors.New(ur.Error)
	}
	return fmt.Errorf("%s failed", what)
}

func (a *clusterAdapter) RemoteContainerAction(ctx context.Context, hostID, containerName, action string) error {
	return a.srv.ContainerActionSync(ctx, hostID, containerName, action)
}

func (a *clusterAdapter) RemoteContainerLogs(ctx context.Context, hostID, containerName string, lines int) (string, error) {
	return a.srv.FetchLogsSync(ctx, hostID, containerName, lines)
}

func (a *clusterAdapter) RollbackRemoteContainer(ctx context.Context, hostID, containerName string) error {
	// Look up the most recent successful update from history to find the old image.
	oldImage, err := lastRemoteOldImage(a.store, hostID, containerName)
	if err != nil {
		return err
	}
	if oldImage == "" {
		return fmt.Errorf("no successful update history found for %s, cannot determine rollback image", containerName)
	}

	hostName := hostID
	rec := store.UpdateRecord{
		ContainerName: store.ScopedKey(hostID, containerName),
		NewImage:      oldImage,
		HostID:        hostID,
	}
	if hs, ok := a.srv.GetHost(hostID); ok {
		hostName, rec.HostName = hs.Info.Name, hs.Info.Name
		for _, c := range hs.Containers {
			if c.Name == containerName {
				rec.OldImage, rec.OldDigest = c.Image, c.ImageDigest
			}
		}
	}

	// Use the existing update mechanism to switch back to the old image.
	// This records and notifies the rollback, so the hub does not record it.
	start := time.Now()
	ur, err := a.srv.RollbackContainerSync(clusterserver.WithCallerRecords(ctx), hostID, containerName, oldImage)
	if err == nil {
		err = remoteResultErr(ur, "rollback")
	}
	if ur.GetOutcome() != "skipped" {
		a.recordRemoteRollback(context.WithoutCancel(ctx), containerName, hostName, rec, ur, err, start)
	}
	return err
}

// recordRemoteRollback records the rollback of a remote container asked for
// from the dashboard or the API, as a local rollback row
// (cluster.RollbackRow), and notifies it. rec holds what was known before
// it was sent: the container, the image it ran and the one it goes back
// to. A failure the agent did not answer is recorded at the time its late
// result would be, and only if that result is not in yet.
func (a *clusterAdapter) recordRemoteRollback(ctx context.Context, name, hostName string, rec store.UpdateRecord, ur *proto.UpdateResult, err error, start time.Time) {
	rec.Timestamp, rec.Duration = time.Now(), time.Since(start)
	if ur != nil {
		if ur.GetOldImage() != "" {
			rec.OldImage, rec.OldDigest = ur.GetOldImage(), ur.GetOldDigest()
		}
		if ur.GetNewImage() != "" {
			rec.NewImage = ur.GetNewImage()
		}
		rec.NewDigest = ur.GetNewDigest()
		rec.Outcome, rec.Error = cluster.RollbackRow(ur.GetOutcome(), ur.GetError())
		if d := ur.GetDuration(); d != nil {
			rec.Duration = d.AsDuration()
		}
	} else {
		rec.Outcome, rec.Error = cluster.RollbackRow("failed", err.Error())
		rec.Timestamp = cluster.HistoryTime(err, rec.Timestamp)
	}
	var recErr error
	if cluster.Unanswered(err) {
		_, recErr = a.store.RecordUpdateIfAbsent(rec)
	} else {
		recErr = a.store.RecordUpdate(rec)
	}
	if recErr != nil && a.log != nil {
		a.log.Warn("failed to record a remote rollback", "hostID", rec.HostID, "container", name, "error", recErr)
	}
	if a.updater != nil {
		a.updater.NotifyRemoteRollback(ctx, hostName, name, rec.OldImage, rec.NewImage, err)
	}
}

// lastRemoteOldImage returns the image a rollback of containerName on hostID
// goes back to: the old image of its newest successful update that has not
// been rolled back already, or "" if there is none. Each rollback asked for
// from the dashboard or API (outcome "rollback" with the error text
// cluster.RolledBackOnRequest) cancels the update before it, so a container
// just rolled back has no target, or the next older one. Rows from before
// v3.0.0 that are rollbacks (outcome "success", type "rollback") count as
// rollbacks too. Remote history rows are keyed
// "hostID::name" (rows from before v3.0.0 are moved to that key when the
// cluster starts, migrateRemoteHistory). The dashboard's Rollback button uses
// the same rule (web remoteRollbackTarget); both use cluster.RollbackTarget.
func lastRemoteOldImage(st *store.Store, hostID, containerName string) (string, error) {
	recs, err := st.ListHistoryByContainer(store.ScopedKey(hostID, containerName), cluster.RollbackHistoryDepth)
	if err != nil {
		return "", fmt.Errorf("lookup update history for %s on %s: %w", containerName, hostID, err)
	}
	rows := make([]cluster.HistoryRow, len(recs))
	for i, rec := range recs {
		rows[i] = cluster.HistoryRow{Outcome: rec.Outcome, Error: rec.Error, Type: rec.Type, OldImage: rec.OldImage}
	}
	return cluster.RollbackTarget(rows), nil
}

// remoteUpdatePrep checks and completes every update request the hub sends
// to an agent (clusterserver.UpdatePrep), whatever started it: a scan, the
// dashboard or the API.
type remoteUpdatePrep struct {
	db      *store.Store
	updater *engine.Updater
	// container returns what the hub last heard about a remote container.
	container func(hostID, name string) (cluster.ContainerInfo, bool)
}

func (p *remoteUpdatePrep) prepare(ctx context.Context, hostID string, req *proto.UpdateContainerRequest, rollback bool) error {
	name := req.GetContainerName()
	c, _ := p.container(hostID, name)
	if !rollback {
		// The signature gate of a local update, before the agent is asked
		// to do anything. A rollback restores an image that ran before.
		image := req.GetTargetImage()
		if image == "" {
			image = c.Image
		}
		if err := p.updater.VerifyRemoteImage(ctx, name, c.Labels, image); err != nil {
			return err
		}
	}
	req.PreviousImages = previousImages(p.db, hostID, name, c.ImageDigest)
	if rollback {
		// A rollback runs no hooks, as locally.
		req.SkipHooks = true
		return nil
	}
	req.Hooks = storedHooks(p.db, hostID, name)
	return nil
}

// storedHooks returns the lifecycle hooks the hub stores for a remote
// container (under its host-scoped key), for the agent to run around the
// update. They win over the container's labels there, as here.
func storedHooks(db *store.Store, hostID, name string) []*proto.HookSpec {
	entries, err := db.ListHooks(store.ScopedKey(hostID, name))
	if err != nil {
		return nil
	}
	out := make([]*proto.HookSpec, 0, len(entries))
	for _, e := range entries {
		out = append(out, &proto.HookSpec{Phase: e.Phase, Command: e.Command, TimeoutSeconds: int32(min(max(e.Timeout, 0), 1<<20))}) //nolint:gosec // clamped
	}
	return out
}

// maxPreviousImages bounds the images sent with an update.
const maxPreviousImages = 10

// previousImages returns the images a remote container ran before its
// current one (whose digest is current), from the hub's update history:
// newest first, each once, at most maxPreviousImages. A row without the
// old image's digest names no image for certain and is skipped.
func previousImages(db *store.Store, hostID, name, current string) []*proto.PreviousImage {
	recs, err := db.ListHistoryByContainer(store.ScopedKey(hostID, name), 100)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	if current != "" {
		seen[current] = true
	}
	var out []*proto.PreviousImage
	for _, rec := range recs { // newest first
		if rec.OldDigest == "" || seen[rec.OldDigest] {
			continue
		}
		seen[rec.OldDigest] = true
		out = append(out, &proto.PreviousImage{Ref: rec.OldImage, Digest: rec.OldDigest})
		if len(out) == maxPreviousImages {
			break
		}
	}
	return out
}

// clusterListenPort is the port the cluster server listens on:
// SENTINEL_CLUSTER_PORT when it is set (it wins, as SENTINEL_CLUSTER_ENABLED
// does; the cluster settings form saves its port with every change), else
// the saved setting, else 9443.
func clusterListenPort(envPort, saved string) string {
	switch {
	case envPort != "":
		return envPort
	case saved != "":
		return saved
	}
	return "9443"
}

// settingsSyncFrom is the SettingsSync message of the hub's settings.
func settingsSyncFrom(st engine.AgentSettings) *proto.SettingsSync {
	return &proto.SettingsSync{
		PollInterval:     durationpb.New(st.PollInterval),
		GracePeriod:      durationpb.New(st.GracePeriod),
		LatestAutoUpdate: st.LatestAutoUpdate,
		ImageCleanup:     st.ImageCleanup,
		HooksEnabled:     st.HooksEnabled,
		DependencyAware:  st.DependencyAware,
		RollbackPolicy:   st.RollbackPolicy,
		RemoveVolumes:    st.RemoveVolumes,
		ImageBackup:      st.ImageBackup,
	}
}

// UpdateInFlight reports whether the hub is waiting for an agent to update
// a container, so a scan leaves that container to it.
func (a *clusterScannerAdapter) UpdateInFlight(hostID, name string) bool {
	return a.srv.UpdateInFlight(hostID, name)
}

// HostPaused reports whether a host is paused (no new updates).
func (a *clusterScannerAdapter) HostPaused(hostID string) bool {
	return a.srv.HostPaused(hostID)
}

// PullImage asks an agent to pull an image for a container (a pull-only
// update) and says how it went.
func (a *clusterScannerAdapter) PullImage(ctx context.Context, hostID, name, ref string) error {
	return a.srv.PullImageSync(ctx, hostID, name, ref)
}

// CheckRemoteForUpdate checks a remote container's image against the
// digest its agent reported, with its labels' semver scope and tag
// filters, as the remote scan does.
func (a *registryCheckerAdapter) CheckRemoteForUpdate(ctx context.Context, imageRef, localDigest string, labels map[string]string) (web.RemoteCheck, error) {
	include, exclude := docker.ContainerTagFilters(labels)
	res := a.checker.CheckVersionedWithDigest(ctx, imageRef, localDigest, docker.ContainerSemverScope(labels), include, exclude)
	if res.Error != nil {
		return web.RemoteCheck{}, res.Error
	}
	if res.IsLocal {
		return web.RemoteCheck{}, nil
	}
	return web.RemoteCheck{
		UpdateAvailable:        res.UpdateAvailable,
		NewerVersions:          res.NewerVersions,
		ResolvedCurrentVersion: res.ResolvedCurrentVersion,
		ResolvedTargetVersion:  res.ResolvedTargetVersion,
		RemoteDigest:           res.RemoteDigest,
	}, nil
}

// wireSelfUpdateNotify has srv notify the outcome of an agent's update of
// itself through u, as the dashboard notifies other remote updates: the hub
// learns the outcome only after the agent comes back.
func wireSelfUpdateNotify(srv *clusterserver.Server, u *engine.Updater) {
	if u == nil {
		return
	}
	srv.SetSelfUpdateNotify(func(hostName, name string, rec store.UpdateRecord) {
		u.NotifyRemote(context.Background(), hostName, name, rec.OldImage, &engine.RemoteUpdateResult{
			ContainerName: name, OldImage: rec.OldImage, OldDigest: rec.OldDigest, NewImage: rec.NewImage,
			NewDigest: rec.NewDigest, Outcome: rec.Outcome, Error: rec.Error, Duration: rec.Duration,
		})
	})
}

// knownContainer returns what srv last heard about a remote container.
func knownContainer(srv *clusterserver.Server) func(hostID, name string) (cluster.ContainerInfo, bool) {
	return func(hostID, name string) (cluster.ContainerInfo, bool) {
		hs, ok := srv.GetHost(hostID)
		if !ok {
			return cluster.ContainerInfo{}, false
		}
		for _, c := range hs.Containers {
			if c.Name == name {
				return c, true
			}
		}
		return cluster.ContainerInfo{}, false
	}
}

// migrateRemoteHistory moves remote history rows written before v3.0.0
// (plain container name with the host ID set) to the "hostID::name" key.
// It is cheap and safe to run on every cluster start.
func migrateRemoteHistory(db *store.Store, log *slog.Logger) {
	n, err := db.MigrateRemoteHistoryKeys()
	if err != nil {
		log.Warn("could not move old remote history rows to their host-scoped key", "error", err)
		return
	}
	if n > 0 {
		log.Info("moved old remote history rows to their host-scoped key", "rows", n)
	}
}

// clusterManager implements web.ClusterLifecycle for dynamic cluster
// start/stop from the settings API. Uses ClusterController.SetProvider()
// to swap the active provider atomically — no value-copy issues.
type clusterManager struct {
	// opMu serialises Start and Stop. mu guards srv only and is never held
	// across a blocking call, so the scheduler's post-scan callback, which
	// takes mu to read srv, cannot stall behind a slow shutdown.
	opMu    sync.Mutex
	mu      sync.Mutex
	srv     *clusterserver.Server
	db      *store.Store
	bus     *events.Bus
	log     *slog.Logger
	updater *engine.Updater
	ctrl    *web.ClusterController // stable pointer in Dependencies
	dataDir string                 // CA/cert storage directory
	envPort string                 // SENTINEL_CLUSTER_PORT, when set
	// registryAuth returns the X-Registry-Auth value for an image: the stored
	// credential, or a cloud provider's (ECR, ACR, GCR).
	registryAuth func(imageRef string) string
}

func (m *clusterManager) Start() error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.srv != nil {
		return nil // already running
	}

	saved, _ := m.db.LoadSetting(store.SettingClusterPort)
	port := clusterListenPort(m.envPort, saved)

	migrateRemoteHistory(m.db, m.log)

	ca, err := cluster.EnsureCA(m.dataDir)
	if err != nil {
		return fmt.Errorf("initialise CA: %w", err)
	}

	m.srv, err = clusterserver.New(ca, m.db, m.bus, m.log)
	if err != nil {
		return fmt.Errorf("create cluster server: %w", err)
	}
	m.srv.SetHistoryRecorder(m.db)
	// Updates carry the stored credentials of the target registry to the agent.
	m.srv.SetRegistryAuth(m.registryAuth)
	prep := &remoteUpdatePrep{db: m.db, updater: m.updater, container: knownContainer(m.srv)}
	m.srv.SetUpdatePrep(prep.prepare)
	// Agents follow the hub's update settings (their own environment wins).
	updater := m.updater
	m.srv.SetSettingsSource(func() *proto.SettingsSync { return settingsSyncFrom(updater.AgentSettings()) })
	wireSelfUpdateNotify(m.srv, m.updater)
	m.srv.SetHostRefreshed(func(hostID string, containers []cluster.ContainerInfo) {
		names := make([]string, len(containers))
		for i, c := range containers {
			names[i] = c.Name
		}
		updater.PruneRemoteQueue(hostID, names)
	})

	// Read advertise addresses for TLS cert SANs. Inside Docker, the container
	// only sees its bridge network IPs, but agents connect via the host's
	// external IP. Check env var first, then DB setting.
	adv, _ := m.db.LoadSetting(store.SettingClusterAdvertise)
	if adv == "" {
		adv = os.Getenv("SENTINEL_CLUSTER_ADVERTISE")
	}
	var extraSANs []string
	for _, s := range strings.Split(adv, ",") {
		if t := strings.TrimSpace(s); t != "" {
			extraSANs = append(extraSANs, t)
		}
	}

	addr := net.JoinHostPort("", port)
	if err := m.srv.Start(addr, extraSANs...); err != nil {
		m.srv = nil
		return fmt.Errorf("start gRPC: %w", err)
	}

	// Wire cluster scanner into the engine for multi-host scanning.
	m.updater.SetClusterScanner(&clusterScannerAdapter{srv: m.srv})

	// Swap provider in controller — handlers see it immediately.
	m.ctrl.SetProvider(&clusterAdapter{srv: m.srv, store: m.db, updater: m.updater, log: m.log})

	m.log.Info("cluster gRPC server started", "addr", addr)
	return nil
}

func (m *clusterManager) Stop() {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	srv := m.srv
	if srv == nil {
		m.mu.Unlock()
		return
	}
	// Clear provider first so handlers stop dispatching.
	m.ctrl.SetProvider(nil)
	m.updater.SetClusterScanner(nil)
	m.srv = nil
	m.mu.Unlock()

	srv.Stop()

	m.log.Info("cluster gRPC server stopped")
}

// --- NPM adapter ---

type npmAdapter struct {
	resolver *npm.Resolver
	stop     context.CancelFunc // ends the resolver's sync loop; nil for the one built at startup
}

// Stop ends the resolver's background sync, when the provider is replaced.
func (a *npmAdapter) Stop() {
	if a.stop != nil {
		a.stop()
	}
}

func (a *npmAdapter) TestConnection(ctx context.Context) error {
	return a.resolver.Sync(ctx)
}

func (a *npmAdapter) Lookup(hostPort uint16) *web.NPMResolvedURL {
	r := a.resolver.Lookup(hostPort)
	if r == nil {
		return nil
	}
	return &web.NPMResolvedURL{
		URL:         r.URL,
		Domain:      r.Domain,
		ProxyHostID: r.ProxyHostID,
	}
}

func (a *npmAdapter) LookupForHost(hostPort uint16, hostAddr string) *web.NPMResolvedURL {
	r := a.resolver.LookupForHost(hostPort, hostAddr)
	if r == nil {
		return nil
	}
	return &web.NPMResolvedURL{
		URL:         r.URL,
		Domain:      r.Domain,
		ProxyHostID: r.ProxyHostID,
	}
}

func (a *npmAdapter) AllMappings() map[uint16]web.NPMResolvedURL {
	raw := a.resolver.AllMappings()
	result := make(map[uint16]web.NPMResolvedURL, len(raw))
	for k, v := range raw {
		result[k] = web.NPMResolvedURL{
			URL:         v.URL,
			Domain:      v.Domain,
			ProxyHostID: v.ProxyHostID,
		}
	}
	return result
}

func (a *npmAdapter) AllMappingsGrouped() map[string]map[uint16]web.NPMResolvedURL {
	raw := a.resolver.AllMappingsGrouped()
	result := make(map[string]map[uint16]web.NPMResolvedURL, len(raw))
	for host, portMap := range raw {
		inner := make(map[uint16]web.NPMResolvedURL, len(portMap))
		for port, v := range portMap {
			inner[port] = web.NPMResolvedURL{
				URL:         v.URL,
				Domain:      v.Domain,
				ProxyHostID: v.ProxyHostID,
			}
		}
		result[host] = inner
	}
	return result
}

func (a *npmAdapter) Sync(ctx context.Context) error {
	return a.resolver.Sync(ctx)
}

func (a *npmAdapter) LastSync() time.Time {
	return a.resolver.LastSync()
}

func (a *npmAdapter) LastError() error {
	return a.resolver.LastError()
}

// hostNames maps host IDs to display names, falling back to the ID.
func (a *clusterAdapter) hostNames(ids []string) []string {
	var names []string
	for _, id := range ids {
		if h, ok := a.srv.GetHost(id); ok && h.Info.Name != "" {
			names = append(names, h.Info.Name)
		} else {
			names = append(names, id)
		}
	}
	return names
}
