package main

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Will-Luck/Docker-Sentinel/internal/backup"
	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/web"
)

// storeAdapter converts store.Store to web.HistoryStore.
type storeAdapter struct{ s *store.Store }

func (a *storeAdapter) ListHistory(limit int, before string) ([]web.UpdateRecord, error) {
	records, err := a.s.ListHistory(limit, before)
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

func (a *storeAdapter) ListAllHistory() ([]web.UpdateRecord, error) {
	records, err := a.s.ListAllHistory()
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

// portConfigStoreAdapter bridges store.Store to web.PortConfigStore.
type portConfigStoreAdapter struct {
	s *store.Store
}

func (a *portConfigStoreAdapter) GetPortConfig(name string) (*web.PortConfig, error) {
	pc, err := a.s.GetPortConfig(name)
	if err != nil || pc == nil {
		return nil, err
	}
	result := &web.PortConfig{Ports: make(map[string]web.PortOverride, len(pc.Ports))}
	for k, v := range pc.Ports {
		result.Ports[k] = web.PortOverride{URL: v.URL, Path: v.Path}
	}
	return result, nil
}

func (a *portConfigStoreAdapter) SetPortOverride(name string, hostPort uint16, override web.PortOverride) error {
	return a.s.SetPortOverride(name, hostPort, store.PortOverride{URL: override.URL, Path: override.Path})
}

func (a *portConfigStoreAdapter) DeletePortOverride(name string, hostPort uint16) error {
	return a.s.DeletePortOverride(name, hostPort)
}

func (a *portConfigStoreAdapter) AllPortConfigs() (map[string]*web.PortConfig, error) {
	raw, err := a.s.AllPortConfigs()
	if err != nil {
		return nil, err
	}
	result := make(map[string]*web.PortConfig, len(raw))
	for name, pc := range raw {
		wpc := &web.PortConfig{Ports: make(map[string]web.PortOverride, len(pc.Ports))}
		for k, v := range pc.Ports {
			wpc.Ports[k] = web.PortOverride{URL: v.URL, Path: v.Path}
		}
		result[name] = wpc
	}
	return result, nil
}

// backupAdapter bridges backup.Manager to web.BackupProvider.
type backupAdapter struct {
	m *backup.Manager
}

func (a *backupAdapter) CreateBackup(ctx context.Context) (*web.BackupInfo, error) {
	info, err := a.m.CreateBackup(ctx)
	if err != nil {
		return nil, err
	}
	return &web.BackupInfo{
		Filename:  info.Filename,
		Size:      info.Size,
		CreatedAt: info.CreatedAt,
	}, nil
}

func (a *backupAdapter) List() ([]web.BackupInfo, error) {
	list, err := a.m.List()
	if err != nil {
		return nil, err
	}
	result := make([]web.BackupInfo, len(list))
	for i, info := range list {
		result[i] = web.BackupInfo{
			Filename:  info.Filename,
			Size:      info.Size,
			CreatedAt: info.CreatedAt,
		}
	}
	return result, nil
}

func (a *backupAdapter) FilePath(filename string) (string, error) {
	return a.m.FilePath(filename)
}

// notifyConfigAdapter bridges store.Store to web.NotificationConfigStore.
type notifyConfigAdapter struct{ s *store.Store }

func (a *notifyConfigAdapter) GetNotificationChannels() ([]notify.Channel, error) {
	return a.s.GetNotificationChannels()
}

func (a *notifyConfigAdapter) SetNotificationChannels(channels []notify.Channel) error {
	return a.s.SetNotificationChannels(channels)
}

// notifyTemplateAdapter bridges store.Store to web.NotifyTemplateStore.
type notifyTemplateAdapter struct{ s *store.Store }

func (a *notifyTemplateAdapter) GetAllNotifyTemplates() (map[string]string, error) {
	return a.s.GetAllNotifyTemplates()
}

func (a *notifyTemplateAdapter) SaveNotifyTemplate(eventType, tmpl string) error {
	return a.s.SaveNotifyTemplate(eventType, tmpl)
}

func (a *notifyTemplateAdapter) DeleteNotifyTemplate(eventType string) error {
	return a.s.DeleteNotifyTemplate(eventType)
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

// releaseSourceAdapter bridges store.Store to web.ReleaseSourceStore.
type releaseSourceAdapter struct{ s *store.Store }

func (a *releaseSourceAdapter) GetReleaseSources() ([]web.ReleaseSource, error) {
	srcs, err := a.s.GetReleaseSources()
	if err != nil {
		return nil, err
	}
	result := make([]web.ReleaseSource, len(srcs))
	for i, src := range srcs {
		result[i] = web.ReleaseSource{
			ImagePattern: src.ImagePattern,
			GitHubRepo:   src.GitHubRepo,
		}
	}
	return result, nil
}

func (a *releaseSourceAdapter) SetReleaseSources(sources []web.ReleaseSource) error {
	regSrcs := make([]registry.ReleaseSource, len(sources))
	for i, src := range sources {
		regSrcs[i] = registry.ReleaseSource{
			ImagePattern: src.ImagePattern,
			GitHubRepo:   src.GitHubRepo,
		}
	}
	return a.s.SetReleaseSources(regSrcs)
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
