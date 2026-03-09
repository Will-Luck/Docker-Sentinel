package main

import (
	"context"
	"fmt"
	"time"

	clusterserver "github.com/Will-Luck/Docker-Sentinel/internal/cluster/server"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/web"
)

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

// tagListerAdapter bridges registry.ListTags to web.RegistryTagLister.
type tagListerAdapter struct {
	log *logging.Logger
}

func (a *tagListerAdapter) ListAllTags(ctx context.Context, imageRef string) ([]string, error) {
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
	return tagsResult.Tags, nil
}

// registryCheckerAdapter bridges registry.Checker to web.RegistryChecker.
type registryCheckerAdapter struct {
	checker *registry.Checker
}

func (a *registryCheckerAdapter) CheckForUpdate(ctx context.Context, imageRef string) (bool, []string, string, string, error) {
	result := a.checker.CheckVersioned(ctx, imageRef, docker.ScopeDefault, "", "")
	if result.Error != nil {
		return false, nil, "", "", result.Error
	}
	if result.IsLocal {
		return false, nil, "", "", nil
	}
	return result.UpdateAvailable, result.NewerVersions, result.ResolvedCurrentVersion, result.ResolvedTargetVersion, nil
}

// versionScopeAdapter bridges registry.Checker to web.VersionScopeUpdater.
type versionScopeAdapter struct {
	checker *registry.Checker
}

func (a *versionScopeAdapter) SetDefaultScope(scope string) {
	if scope == "strict" {
		a.checker.SetDefaultScope(docker.ScopeStrict)
	} else {
		a.checker.SetDefaultScope(docker.ScopeDefault)
	}
}

// selfUpdateAdapter bridges engine.SelfUpdater to web.SelfUpdater.
type selfUpdateAdapter struct {
	updater *engine.SelfUpdater
}

func (a *selfUpdateAdapter) Update(ctx context.Context, targetImage string) error {
	return a.updater.Update(ctx, targetImage)
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
