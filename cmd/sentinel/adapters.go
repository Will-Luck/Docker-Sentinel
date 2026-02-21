package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	clusterserver "github.com/Will-Luck/Docker-Sentinel/internal/cluster/server"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/web"
)

// --- Adapters bridging concrete types to web.Dependencies interfaces ---

// storeAdapter converts store.Store to web.HistoryStore.
type storeAdapter struct{ s *store.Store }

func (a *storeAdapter) ListHistory(limit int) ([]web.UpdateRecord, error) {
	records, err := a.s.ListHistory(limit)
	if err != nil {
		return nil, err
	}
	result := make([]web.UpdateRecord, len(records))
	for i, r := range records {
		result[i] = web.UpdateRecord{
			Timestamp:     r.Timestamp,
			ContainerName: r.ContainerName,
			OldImage:      r.OldImage,
			OldDigest:     r.OldDigest,
			NewImage:      r.NewImage,
			NewDigest:     r.NewDigest,
			Outcome:       r.Outcome,
			Duration:      r.Duration,
			Error:         r.Error,
			Type:          r.Type,
			HostID:        r.HostID,
			HostName:      r.HostName,
		}
	}
	return result, nil
}

func (a *storeAdapter) ListHistoryByContainer(name string, limit int) ([]web.UpdateRecord, error) {
	records, err := a.s.ListHistoryByContainer(name, limit)
	if err != nil {
		return nil, err
	}
	result := make([]web.UpdateRecord, len(records))
	for i, r := range records {
		result[i] = web.UpdateRecord{
			Timestamp:     r.Timestamp,
			ContainerName: r.ContainerName,
			OldImage:      r.OldImage,
			OldDigest:     r.OldDigest,
			NewImage:      r.NewImage,
			NewDigest:     r.NewDigest,
			Outcome:       r.Outcome,
			Duration:      r.Duration,
			Error:         r.Error,
			Type:          r.Type,
			HostID:        r.HostID,
			HostName:      r.HostName,
		}
	}
	return result, nil
}

func (a *storeAdapter) GetMaintenance(name string) (bool, error) {
	return a.s.GetMaintenance(name)
}

func (a *storeAdapter) RecordUpdate(rec web.UpdateRecord) error {
	return a.s.RecordUpdate(store.UpdateRecord{
		Timestamp:     rec.Timestamp,
		ContainerName: rec.ContainerName,
		OldImage:      rec.OldImage,
		OldDigest:     rec.OldDigest,
		NewImage:      rec.NewImage,
		NewDigest:     rec.NewDigest,
		Outcome:       rec.Outcome,
		Duration:      rec.Duration,
		Error:         rec.Error,
		Type:          rec.Type,
		HostID:        rec.HostID,
		HostName:      rec.HostName,
	})
}

// aboutStoreAdapter converts store.Store to web.AboutStore.
type aboutStoreAdapter struct{ s *store.Store }

func (a *aboutStoreAdapter) CountHistory() (int, error)   { return a.s.CountHistory() }
func (a *aboutStoreAdapter) CountSnapshots() (int, error) { return a.s.CountSnapshots() }

// snapshotAdapter converts store.Store to web.SnapshotStore.
type snapshotAdapter struct{ s *store.Store }

func (a *snapshotAdapter) ListSnapshots(name string) ([]web.SnapshotEntry, error) {
	entries, err := a.s.ListSnapshots(name)
	if err != nil {
		return nil, err
	}
	result := make([]web.SnapshotEntry, len(entries))
	for i, e := range entries {
		// Extract image reference from the snapshot JSON data.
		imageRef := extractImageFromSnapshot(e.Data)
		result[i] = web.SnapshotEntry{
			Timestamp: e.Timestamp,
			ImageRef:  imageRef,
		}
	}
	return result, nil
}

// extractImageFromSnapshot parses the image reference from a container inspect JSON snapshot.
func extractImageFromSnapshot(data []byte) string {
	var snap struct {
		Config *struct {
			Image string `json:"Image"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		return ""
	}
	if snap.Config != nil {
		return snap.Config.Image
	}
	return ""
}

// queueAdapter converts engine.Queue to web.UpdateQueue.
type queueAdapter struct{ q *engine.Queue }

func (a *queueAdapter) List() []web.PendingUpdate {
	items := a.q.List()
	result := make([]web.PendingUpdate, len(items))
	for i, item := range items {
		result[i] = convertPendingUpdate(item)
	}
	return result
}

func (a *queueAdapter) Add(update web.PendingUpdate) {
	a.q.Add(engine.PendingUpdate{
		ContainerID:            update.ContainerID,
		ContainerName:          update.ContainerName,
		CurrentImage:           update.CurrentImage,
		CurrentDigest:          update.CurrentDigest,
		RemoteDigest:           update.RemoteDigest,
		DetectedAt:             update.DetectedAt,
		NewerVersions:          update.NewerVersions,
		ResolvedCurrentVersion: update.ResolvedCurrentVersion,
		ResolvedTargetVersion:  update.ResolvedTargetVersion,
		Type:                   update.Type,
		HostID:                 update.HostID,
		HostName:               update.HostName,
	})
}

func (a *queueAdapter) Get(name string) (web.PendingUpdate, bool) {
	item, ok := a.q.Get(name)
	if !ok {
		return web.PendingUpdate{}, false
	}
	return convertPendingUpdate(item), true
}

func (a *queueAdapter) Approve(name string) (web.PendingUpdate, bool) {
	item, ok := a.q.Approve(name)
	if !ok {
		return web.PendingUpdate{}, false
	}
	return convertPendingUpdate(item), true
}

func (a *queueAdapter) Remove(name string) { a.q.Remove(name) }

func convertPendingUpdate(item engine.PendingUpdate) web.PendingUpdate {
	return web.PendingUpdate{
		ContainerID:            item.ContainerID,
		ContainerName:          item.ContainerName,
		CurrentImage:           item.CurrentImage,
		CurrentDigest:          item.CurrentDigest,
		RemoteDigest:           item.RemoteDigest,
		DetectedAt:             item.DetectedAt,
		NewerVersions:          item.NewerVersions,
		ResolvedCurrentVersion: item.ResolvedCurrentVersion,
		ResolvedTargetVersion:  item.ResolvedTargetVersion,
		Type:                   item.Type,
		HostID:                 item.HostID,
		HostName:               item.HostName,
	}
}

// dockerAdapter converts docker.Client to web.ContainerLister.
type dockerAdapter struct{ c *docker.Client }

func (a *dockerAdapter) ListContainers(ctx context.Context) ([]web.ContainerSummary, error) {
	containers, err := a.c.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]web.ContainerSummary, len(containers))
	for i, c := range containers {
		result[i] = web.ContainerSummary{
			ID:     c.ID,
			Names:  c.Names,
			Image:  c.Image,
			Labels: c.Labels,
			State:  string(c.State),
		}
	}
	return result, nil
}

func (a *dockerAdapter) ListAllContainers(ctx context.Context) ([]web.ContainerSummary, error) {
	containers, err := a.c.ListAllContainers(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]web.ContainerSummary, len(containers))
	for i, c := range containers {
		result[i] = web.ContainerSummary{
			ID:     c.ID,
			Names:  c.Names,
			Image:  c.Image,
			Labels: c.Labels,
			State:  string(c.State),
		}
	}
	return result, nil
}

func (a *dockerAdapter) InspectContainer(ctx context.Context, id string) (web.ContainerInspect, error) {
	inspect, err := a.c.InspectContainer(ctx, id)
	if err != nil {
		return web.ContainerInspect{}, err
	}
	var ci web.ContainerInspect
	ci.ID = inspect.ID
	ci.Name = inspect.Name
	if inspect.Config != nil {
		ci.Image = inspect.Config.Image
	}
	if inspect.State != nil {
		ci.State.Status = string(inspect.State.Status)
		ci.State.Running = inspect.State.Running
		ci.State.Restarting = inspect.State.Restarting
	}
	return ci, nil
}

// restartAdapter bridges docker.Client to web.ContainerRestarter.
type restartAdapter struct{ c *docker.Client }

func (a *restartAdapter) RestartContainer(ctx context.Context, id string) error {
	return a.c.RestartContainer(ctx, id)
}

// rollbackAdapter bridges engine.RollbackFromStore to web.ContainerRollback.
type rollbackAdapter struct {
	d   *docker.Client
	s   *store.Store
	log *logging.Logger
}

func (a *rollbackAdapter) RollbackContainer(ctx context.Context, name string) error {
	return engine.RollbackFromStore(ctx, a.d, a.s, name, a.log)
}

// registryAdapter bridges registry.ListTags to web.RegistryVersionChecker.
type registryAdapter struct {
	log *logging.Logger
}

func (a *registryAdapter) ListVersions(ctx context.Context, imageRef string) ([]string, error) {
	tag := registry.ExtractTag(imageRef)
	if tag == "" {
		return nil, nil
	}
	repo := registry.RepoPath(imageRef)
	host := registry.RegistryHost(imageRef)

	token, err := registry.FetchToken(ctx, repo, nil, host)
	if err != nil {
		return nil, fmt.Errorf("fetch token: %w", err)
	}
	tagsResult, err := registry.ListTags(ctx, imageRef, token, host, nil)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	// Filter to semver-parseable tags and return newest first.
	newer := registry.NewerVersions(tag, tagsResult.Tags)
	versions := make([]string, len(newer))
	for i, sv := range newer {
		versions[i] = sv.Raw
	}
	return versions, nil
}

// registryCheckerAdapter bridges registry.Checker to web.RegistryChecker.
type registryCheckerAdapter struct {
	checker *registry.Checker
}

func (a *registryCheckerAdapter) CheckForUpdate(ctx context.Context, imageRef string) (bool, []string, string, string, error) {
	result := a.checker.CheckVersioned(ctx, imageRef)
	if result.Error != nil {
		return false, nil, "", "", result.Error
	}
	if result.IsLocal {
		return false, nil, "", "", nil
	}
	return result.UpdateAvailable, result.NewerVersions, result.ResolvedCurrentVersion, result.ResolvedTargetVersion, nil
}

// policyStoreAdapter bridges store.Store to web.PolicyStore.
type policyStoreAdapter struct{ s *store.Store }

func (a *policyStoreAdapter) GetPolicyOverride(name string) (string, bool) {
	return a.s.GetPolicyOverride(name)
}

func (a *policyStoreAdapter) SetPolicyOverride(name, policy string) error {
	return a.s.SetPolicyOverride(name, policy)
}

func (a *policyStoreAdapter) DeletePolicyOverride(name string) error {
	return a.s.DeletePolicyOverride(name)
}

func (a *policyStoreAdapter) AllPolicyOverrides() map[string]string {
	return a.s.AllPolicyOverrides()
}

// eventLogAdapter bridges store.Store to web.EventLogger.
type eventLogAdapter struct{ s *store.Store }

func (a *eventLogAdapter) AppendLog(entry web.LogEntry) error {
	return a.s.AppendLog(store.LogEntry{
		Timestamp: entry.Timestamp,
		Type:      entry.Type,
		Message:   entry.Message,
		Container: entry.Container,
		User:      entry.User,
		Kind:      entry.Kind,
	})
}

func (a *eventLogAdapter) ListLogs(limit int) ([]web.LogEntry, error) {
	entries, err := a.s.ListLogs(limit)
	if err != nil {
		return nil, err
	}
	result := make([]web.LogEntry, len(entries))
	for i, e := range entries {
		result[i] = web.LogEntry{
			Timestamp: e.Timestamp,
			Type:      e.Type,
			Message:   e.Message,
			Container: e.Container,
			User:      e.User,
			Kind:      e.Kind,
		}
	}
	return result, nil
}

// settingsStoreAdapter bridges store.Store to web.SettingsStore.
type settingsStoreAdapter struct{ s *store.Store }

func (a *settingsStoreAdapter) SaveSetting(key, value string) error {
	return a.s.SaveSetting(key, value)
}

func (a *settingsStoreAdapter) LoadSetting(key string) (string, error) {
	return a.s.LoadSetting(key)
}

func (a *settingsStoreAdapter) GetAllSettings() (map[string]string, error) {
	return a.s.GetAllSettings()
}

// selfUpdateAdapter bridges engine.SelfUpdater to web.SelfUpdater.
type selfUpdateAdapter struct {
	updater *engine.SelfUpdater
}

func (a *selfUpdateAdapter) Update(ctx context.Context, targetImage string) error {
	return a.updater.Update(ctx, targetImage)
}

// stopAdapter bridges docker.Client to web.ContainerStopper.
type stopAdapter struct{ c *docker.Client }

func (a *stopAdapter) StopContainer(ctx context.Context, id string) error {
	return a.c.StopContainer(ctx, id, 10)
}

// startAdapter bridges docker.Client to web.ContainerStarter.
type startAdapter struct{ c *docker.Client }

func (a *startAdapter) StartContainer(ctx context.Context, id string) error {
	return a.c.StartContainer(ctx, id)
}

// notifyConfigAdapter bridges store.Store to web.NotificationConfigStore.
type notifyConfigAdapter struct{ s *store.Store }

func (a *notifyConfigAdapter) GetNotificationChannels() ([]notify.Channel, error) {
	return a.s.GetNotificationChannels()
}

func (a *notifyConfigAdapter) SetNotificationChannels(channels []notify.Channel) error {
	return a.s.SetNotificationChannels(channels)
}

// notifyStateAdapter bridges store.Store to web.NotifyStateStore.
type notifyStateAdapter struct{ s *store.Store }

func (a *notifyStateAdapter) GetNotifyPref(name string) (*web.NotifyPref, error) {
	p, err := a.s.GetNotifyPref(name)
	if err != nil || p == nil {
		return nil, err
	}
	return &web.NotifyPref{Mode: p.Mode}, nil
}

func (a *notifyStateAdapter) SetNotifyPref(name string, pref *web.NotifyPref) error {
	return a.s.SetNotifyPref(name, &store.NotifyPref{Mode: pref.Mode})
}

func (a *notifyStateAdapter) DeleteNotifyPref(name string) error {
	return a.s.DeleteNotifyPref(name)
}

func (a *notifyStateAdapter) AllNotifyPrefs() (map[string]*web.NotifyPref, error) {
	prefs, err := a.s.AllNotifyPrefs()
	if err != nil {
		return nil, err
	}
	result := make(map[string]*web.NotifyPref, len(prefs))
	for k, v := range prefs {
		result[k] = &web.NotifyPref{Mode: v.Mode}
	}
	return result, nil
}

func (a *notifyStateAdapter) AllNotifyStates() (map[string]*web.NotifyState, error) {
	states, err := a.s.AllNotifyStates()
	if err != nil {
		return nil, err
	}
	result := make(map[string]*web.NotifyState, len(states))
	for k, v := range states {
		result[k] = &web.NotifyState{
			LastDigest:   v.LastDigest,
			LastNotified: v.LastNotified,
			FirstSeen:    v.FirstSeen,
		}
	}
	return result, nil
}

func (a *notifyStateAdapter) ClearNotifyState(name string) error {
	return a.s.ClearNotifyState(name)
}

// ignoredVersionAdapter bridges store.Store to web.IgnoredVersionStore.
type ignoredVersionAdapter struct{ s *store.Store }

func (a *ignoredVersionAdapter) AddIgnoredVersion(containerName, version string) error {
	return a.s.AddIgnoredVersion(containerName, version)
}

func (a *ignoredVersionAdapter) GetIgnoredVersions(containerName string) ([]string, error) {
	return a.s.GetIgnoredVersions(containerName)
}

func (a *ignoredVersionAdapter) ClearIgnoredVersions(containerName string) error {
	return a.s.ClearIgnoredVersions(containerName)
}

// registryCredentialAdapter bridges store.Store (which returns registry.RegistryCredential)
// to web.RegistryCredentialStore (which uses web.RegistryCredential).
type registryCredentialAdapter struct{ s *store.Store }

func (a *registryCredentialAdapter) GetRegistryCredentials() ([]web.RegistryCredential, error) {
	creds, err := a.s.GetRegistryCredentials()
	if err != nil {
		return nil, err
	}
	result := make([]web.RegistryCredential, len(creds))
	for i, c := range creds {
		result[i] = web.RegistryCredential{
			ID:       c.ID,
			Registry: c.Registry,
			Username: c.Username,
			Secret:   c.Secret,
		}
	}
	return result, nil
}

func (a *registryCredentialAdapter) SetRegistryCredentials(creds []web.RegistryCredential) error {
	regCreds := make([]registry.RegistryCredential, len(creds))
	for i, c := range creds {
		regCreds[i] = registry.RegistryCredential{
			ID:       c.ID,
			Registry: c.Registry,
			Username: c.Username,
			Secret:   c.Secret,
		}
	}
	return a.s.SetRegistryCredentials(regCreds)
}

// rateLimitAdapter bridges registry.RateLimitTracker to web.RateLimitProvider.
type rateLimitAdapter struct {
	t     *registry.RateLimitTracker
	saver func([]byte) error // optional: persist after probe
}

func (a *rateLimitAdapter) Status() []web.RateLimitStatus {
	statuses := a.t.Status()
	result := make([]web.RateLimitStatus, len(statuses))
	for i, s := range statuses {
		result[i] = web.RateLimitStatus{
			Registry:       s.Registry,
			Limit:          s.Limit,
			Remaining:      s.Remaining,
			ResetAt:        s.ResetAt,
			IsAuth:         s.IsAuth,
			HasLimits:      s.HasLimits,
			ContainerCount: s.ContainerCount,
			LastUpdated:    s.LastUpdated,
		}
	}
	return result
}

func (a *rateLimitAdapter) OverallHealth() string {
	return a.t.OverallHealth()
}

func (a *rateLimitAdapter) ProbeAndRecord(ctx context.Context, host string, cred web.RegistryCredential) error {
	regCred := &registry.RegistryCredential{
		ID:       cred.ID,
		Registry: cred.Registry,
		Username: cred.Username,
		Secret:   cred.Secret,
	}
	headers, err := registry.ProbeRateLimit(ctx, host, regCred)
	if err != nil {
		return err
	}
	a.t.Record(host, headers)
	a.t.SetAuth(host, true)
	// Persist updated rate limits to DB.
	if a.saver != nil {
		if data, exportErr := a.t.Export(); exportErr != nil {
			slog.Warn("failed to export rate limit state", "error", exportErr)
		} else if err := a.saver(data); err != nil {
			slog.Warn("failed to persist rate limit state", "error", err)
		}
	}
	return nil
}

// ghcrCacheAdapter bridges registry.GHCRCache to web.GHCRAlternativeProvider.
type ghcrCacheAdapter struct{ c *registry.GHCRCache }

func (a *ghcrCacheAdapter) Get(repo, tag string) (*web.GHCRAlternative, bool) {
	alt, ok := a.c.Get(repo, tag)
	if !ok {
		return nil, false
	}
	return &web.GHCRAlternative{
		DockerHubImage: alt.DockerHubImage,
		GHCRImage:      alt.GHCRImage,
		Tag:            alt.Tag,
		Available:      alt.Available,
		DigestMatch:    alt.DigestMatch,
		HubDigest:      alt.HubDigest,
		GHCRDigest:     alt.GHCRDigest,
		CheckedAt:      alt.CheckedAt,
	}, true
}

func (a *ghcrCacheAdapter) All() []web.GHCRAlternative {
	alts := a.c.All()
	result := make([]web.GHCRAlternative, len(alts))
	for i, alt := range alts {
		result[i] = web.GHCRAlternative{
			DockerHubImage: alt.DockerHubImage,
			GHCRImage:      alt.GHCRImage,
			Tag:            alt.Tag,
			Available:      alt.Available,
			DigestMatch:    alt.DigestMatch,
			HubDigest:      alt.HubDigest,
			GHCRDigest:     alt.GHCRDigest,
			CheckedAt:      alt.CheckedAt,
		}
	}
	return result
}

// hookStoreAdapter converts store.Store to hooks.Store interface.
type hookStoreAdapter struct{ s *store.Store }

func (a *hookStoreAdapter) ListHooks(containerName string) ([]hooks.Hook, error) {
	entries, err := a.s.ListHooks(containerName)
	if err != nil {
		return nil, err
	}
	result := make([]hooks.Hook, len(entries))
	for i, e := range entries {
		result[i] = hooks.Hook{
			ContainerName: e.ContainerName,
			Phase:         e.Phase,
			Command:       e.Command,
			Timeout:       e.Timeout,
		}
	}
	return result, nil
}

func (a *hookStoreAdapter) SaveHook(hook hooks.Hook) error {
	return a.s.SaveHook(store.HookEntry{
		ContainerName: hook.ContainerName,
		Phase:         hook.Phase,
		Command:       hook.Command,
		Timeout:       hook.Timeout,
	})
}

func (a *hookStoreAdapter) DeleteHook(containerName, phase string) error {
	return a.s.DeleteHook(containerName, phase)
}

// webHookStoreAdapter converts store.Store to web.HookStore interface.
type webHookStoreAdapter struct{ s *store.Store }

func (a *webHookStoreAdapter) ListHooks(containerName string) ([]web.HookEntry, error) {
	entries, err := a.s.ListHooks(containerName)
	if err != nil {
		return nil, err
	}
	result := make([]web.HookEntry, len(entries))
	for i, e := range entries {
		result[i] = web.HookEntry{
			ContainerName: e.ContainerName,
			Phase:         e.Phase,
			Command:       e.Command,
			Timeout:       e.Timeout,
		}
	}
	return result, nil
}

func (a *webHookStoreAdapter) SaveHook(hook web.HookEntry) error {
	return a.s.SaveHook(store.HookEntry{
		ContainerName: hook.ContainerName,
		Phase:         hook.Phase,
		Command:       hook.Command,
		Timeout:       hook.Timeout,
	})
}

func (a *webHookStoreAdapter) DeleteHook(containerName, phase string) error {
	return a.s.DeleteHook(containerName, phase)
}

// swarmAdapter bridges docker.Client + engine.Updater to web.SwarmProvider.
type swarmAdapter struct {
	client  *docker.Client
	updater *engine.Updater
}

func (a *swarmAdapter) IsSwarmMode() bool {
	return a.client.IsSwarmManager(context.Background())
}

func (a *swarmAdapter) ListServices(ctx context.Context) ([]web.ServiceSummary, error) {
	services, err := a.client.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]web.ServiceSummary, len(services))
	for i, svc := range services {
		replicas := ""
		var desired, running uint64
		if svc.ServiceStatus != nil {
			running = svc.ServiceStatus.RunningTasks
			desired = svc.ServiceStatus.DesiredTasks
			replicas = fmt.Sprintf("%d/%d", running, desired)
		}
		result[i] = web.ServiceSummary{
			ID:              svc.ID,
			Name:            svc.Spec.Name,
			Image:           svc.Spec.TaskTemplate.ContainerSpec.Image,
			Labels:          svc.Spec.Labels,
			Replicas:        replicas,
			DesiredReplicas: desired,
			RunningReplicas: running,
		}
	}
	return result, nil
}

func (a *swarmAdapter) ListServiceDetail(ctx context.Context) ([]web.ServiceDetail, error) {
	services, err := a.client.ListServices(ctx)
	if err != nil {
		return nil, err
	}

	// Build node lookup (one call, not per-task).
	nodes, nodeErr := a.client.ListNodes(ctx)
	nodeMap := make(map[string]struct{ Name, Addr string })
	if nodeErr == nil {
		for _, n := range nodes {
			name := n.Description.Hostname
			addr := n.Status.Addr
			nodeMap[n.ID] = struct{ Name, Addr string }{name, addr}
		}
	}

	result := make([]web.ServiceDetail, 0, len(services))
	for _, svc := range services {
		replicas := ""
		var desired, running uint64
		if svc.ServiceStatus != nil {
			running = svc.ServiceStatus.RunningTasks
			desired = svc.ServiceStatus.DesiredTasks
			replicas = fmt.Sprintf("%d/%d", running, desired)
		}

		imageRef := svc.Spec.TaskTemplate.ContainerSpec.Image
		// Strip Swarm digest pinning for display.
		if i := strings.Index(imageRef, "@sha256:"); i > 0 {
			imageRef = imageRef[:i]
		}

		updateStatus := ""
		if svc.UpdateStatus != nil {
			updateStatus = string(svc.UpdateStatus.State)
		}

		summary := web.ServiceSummary{
			ID:              svc.ID,
			Name:            svc.Spec.Name,
			Image:           imageRef,
			Labels:          svc.Spec.Labels,
			Replicas:        replicas,
			DesiredReplicas: desired,
			RunningReplicas: running,
		}

		// Fetch tasks for this service.
		tasks, taskErr := a.client.ListServiceTasks(ctx, svc.ID)
		var taskInfos []web.TaskInfo
		if taskErr == nil {
			taskInfos = make([]web.TaskInfo, 0, len(tasks))
			for _, t := range tasks {
				nodeName := t.NodeID
				nodeAddr := ""
				if info, ok := nodeMap[t.NodeID]; ok {
					nodeName = info.Name
					nodeAddr = info.Addr
				}
				taskImage := t.Spec.ContainerSpec.Image
				if i := strings.Index(taskImage, "@sha256:"); i > 0 {
					taskImage = taskImage[:i]
				}
				tag := ""
				if parts := strings.SplitN(taskImage, ":", 2); len(parts) == 2 {
					tag = parts[1]
				}
				errMsg := ""
				if t.Status.Err != "" {
					errMsg = t.Status.Err
				}
				taskInfos = append(taskInfos, web.TaskInfo{
					NodeID:   t.NodeID,
					NodeName: nodeName,
					NodeAddr: nodeAddr,
					State:    string(t.Status.State),
					Image:    taskImage,
					Tag:      tag,
					Slot:     t.Slot,
					Error:    errMsg,
				})
			}
		}

		result = append(result, web.ServiceDetail{
			ServiceSummary: summary,
			Tasks:          taskInfos,
			UpdateStatus:   updateStatus,
		})
	}
	return result, nil
}

func (a *swarmAdapter) UpdateService(ctx context.Context, id, name, targetImage string) error {
	return a.updater.UpdateService(ctx, id, name, targetImage)
}

func (a *swarmAdapter) RollbackService(ctx context.Context, id, name string) error {
	// Look up service by name if id is empty (rollback from UI).
	if id == "" {
		services, err := a.client.ListServices(ctx)
		if err != nil {
			return fmt.Errorf("list services: %w", err)
		}
		for _, svc := range services {
			if svc.Spec.Name == name {
				id = svc.ID
				break
			}
		}
		if id == "" {
			return fmt.Errorf("service %s not found", name)
		}
	}

	svc, err := a.client.InspectService(ctx, id)
	if err != nil {
		return fmt.Errorf("inspect service %s: %w", name, err)
	}
	return a.client.RollbackService(ctx, id, svc.Meta.Version, svc.Spec)
}

func (a *swarmAdapter) ScaleService(ctx context.Context, name string, replicas uint64) error {
	services, err := a.client.ListServices(ctx)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	var id string
	for _, svc := range services {
		if svc.Spec.Name == name {
			id = svc.ID
			break
		}
	}
	if id == "" {
		return fmt.Errorf("service %s not found", name)
	}

	svc, err := a.client.InspectService(ctx, id)
	if err != nil {
		return fmt.Errorf("inspect service %s: %w", name, err)
	}
	if svc.Spec.Mode.Replicated == nil {
		return fmt.Errorf("service %s is not replicated (global mode)", name)
	}
	svc.Spec.Mode.Replicated.Replicas = &replicas
	return a.client.UpdateService(ctx, id, svc.Meta.Version, svc.Spec, "")
}

// clusterScannerAdapter bridges cluster/server.Server to engine.ClusterScanner.
// This enables the engine's multi-host scanning to send synchronous
// ListContainers and UpdateContainer requests to remote agents.
type clusterScannerAdapter struct {
	srv *clusterserver.Server
}

func (a *clusterScannerAdapter) ConnectedHosts() []string {
	return a.srv.ConnectedHosts()
}

func (a *clusterScannerAdapter) HostInfo(hostID string) (engine.HostContext, bool) {
	hs, ok := a.srv.GetHost(hostID)
	if !ok {
		return engine.HostContext{}, false
	}
	return engine.HostContext{
		HostID:   hs.Info.ID,
		HostName: hs.Info.Name,
	}, true
}

func (a *clusterScannerAdapter) ListContainers(ctx context.Context, hostID string) ([]engine.RemoteContainer, error) {
	containers, err := a.srv.ListContainersSync(ctx, hostID)
	if err != nil {
		return nil, err
	}
	result := make([]engine.RemoteContainer, len(containers))
	for i, c := range containers {
		result[i] = engine.RemoteContainer{
			ID:          c.ID,
			Name:        c.Name,
			Image:       c.Image,
			ImageDigest: c.ImageDigest,
			State:       c.State,
			Labels:      c.Labels,
		}
	}
	return result, nil
}

func (a *clusterScannerAdapter) UpdateContainer(ctx context.Context, hostID, containerName, targetImage, targetDigest string) (engine.RemoteUpdateResult, error) {
	ur, err := a.srv.UpdateContainerSync(ctx, hostID, containerName, targetImage, targetDigest)
	if err != nil {
		return engine.RemoteUpdateResult{}, err
	}
	var dur time.Duration
	if ur.Duration != nil {
		dur = ur.Duration.AsDuration()
	}
	return engine.RemoteUpdateResult{
		ContainerName: ur.ContainerName,
		OldImage:      ur.OldImage,
		OldDigest:     ur.OldDigest,
		NewImage:      ur.NewImage,
		NewDigest:     ur.NewDigest,
		Outcome:       ur.Outcome,
		Error:         ur.Error,
		Duration:      dur,
	}, nil
}

// clusterAdapter bridges cluster/server.Server to web.ClusterProvider.
type clusterAdapter struct {
	srv *clusterserver.Server
}

func (a *clusterAdapter) AllHosts() []web.ClusterHost {
	infos := a.srv.AllHosts()
	result := make([]web.ClusterHost, 0, len(infos))
	for _, h := range infos {
		// Use GetHost to get full HostState (includes ephemeral
		// fields like Connected and in-memory Containers).
		if hs, ok := a.srv.GetHost(h.ID); ok {
			result = append(result, web.ClusterHost{
				ID:           hs.Info.ID,
				Name:         hs.Info.Name,
				Address:      hs.Info.Address,
				State:        string(hs.Info.State),
				Connected:    hs.Connected,
				EnrolledAt:   hs.Info.EnrolledAt,
				LastSeen:     hs.Info.LastSeen,
				AgentVersion: hs.Info.AgentVersion,
				Containers:   len(hs.Containers),
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
		ID:           hs.Info.ID,
		Name:         hs.Info.Name,
		Address:      hs.Info.Address,
		State:        string(hs.Info.State),
		Connected:    hs.Connected,
		EnrolledAt:   hs.Info.EnrolledAt,
		LastSeen:     hs.Info.LastSeen,
		AgentVersion: hs.Info.AgentVersion,
		Containers:   len(hs.Containers),
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
			result = append(result, web.RemoteContainer{
				Name:     c.Name,
				Image:    c.Image,
				State:    c.State,
				HostID:   info.ID,
				HostName: info.Name,
				Labels:   c.Labels,
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

func (a *clusterAdapter) DrainHost(id string) error {
	return a.srv.DrainHost(id)
}

func (a *clusterAdapter) UpdateRemoteContainer(ctx context.Context, hostID, containerName, targetImage, targetDigest string) error {
	_, err := a.srv.UpdateContainerSync(ctx, hostID, containerName, targetImage, targetDigest)
	return err
}

func (a *clusterAdapter) RemoteContainerAction(ctx context.Context, hostID, containerName, action string) error {
	return a.srv.ContainerActionSync(ctx, hostID, containerName, action)
}

// clusterManager implements web.ClusterLifecycle for dynamic cluster
// start/stop from the settings API. Uses ClusterController.SetProvider()
// to swap the active provider atomically — no value-copy issues.
type clusterManager struct {
	mu      sync.Mutex
	srv     *clusterserver.Server
	db      *store.Store
	bus     *events.Bus
	log     *slog.Logger
	updater *engine.Updater
	ctrl    *web.ClusterController // stable pointer in Dependencies
	dataDir string                 // CA/cert storage directory
}

func (m *clusterManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.srv != nil {
		return nil // already running
	}

	// Read port from DB, fall back to default.
	port, _ := m.db.LoadSetting(store.SettingClusterPort)
	if port == "" {
		port = "9443"
	}

	ca, err := cluster.EnsureCA(m.dataDir)
	if err != nil {
		return fmt.Errorf("initialise CA: %w", err)
	}

	m.srv = clusterserver.New(ca, m.db, m.bus, m.log)

	addr := net.JoinHostPort("", port)
	if err := m.srv.Start(addr); err != nil {
		m.srv = nil
		return fmt.Errorf("start gRPC: %w", err)
	}

	// Wire cluster scanner into the engine for multi-host scanning.
	m.updater.SetClusterScanner(&clusterScannerAdapter{srv: m.srv})

	// Swap provider in controller — handlers see it immediately.
	m.ctrl.SetProvider(&clusterAdapter{srv: m.srv})

	m.log.Info("cluster gRPC server started", "addr", addr)
	return nil
}

func (m *clusterManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.srv == nil {
		return
	}

	// Clear provider first so handlers stop dispatching.
	m.ctrl.SetProvider(nil)
	m.updater.SetClusterScanner(nil)

	m.srv.Stop()
	m.srv = nil

	m.log.Info("cluster gRPC server stopped")
}
