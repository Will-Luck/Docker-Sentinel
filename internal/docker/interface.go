package docker

import (
	"context"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
)

// API defines the subset of Docker operations used by Sentinel.
// Implemented by Client for production, and by mocks for testing.
type API interface {
	ListContainers(ctx context.Context) ([]container.Summary, error)
	ListAllContainers(ctx context.Context) ([]container.Summary, error)
	InspectContainer(ctx context.Context, id string) (container.InspectResponse, error)
	StopContainer(ctx context.Context, id string, timeout int) error
	RemoveContainer(ctx context.Context, id string) error
	CreateContainer(ctx context.Context, name string, cfg *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig) (string, error)
	StartContainer(ctx context.Context, id string) error
	RestartContainer(ctx context.Context, id string) error
	PullImage(ctx context.Context, refStr string) error
	ImageDigest(ctx context.Context, imageRef string) (string, error)
	ImageID(ctx context.Context, imageRef string) (string, error)
	DistributionDigest(ctx context.Context, imageRef string) (string, error)
	RemoveImage(ctx context.Context, id string) error
	TagImage(ctx context.Context, src, target string) error
	ListImages(ctx context.Context) ([]ImageSummary, error)
	PruneImages(ctx context.Context) (ImagePruneResult, error)
	RemoveImageByID(ctx context.Context, id string) error
	RemoveContainerWithVolumes(ctx context.Context, id string) error
	ExecContainer(ctx context.Context, id string, cmd []string, timeout int) (int, string, error)
	ContainerLogs(ctx context.Context, id string, lines int) (string, error)

	// Swarm operations — only functional when the daemon is a Swarm manager.
	IsSwarmManager(ctx context.Context) bool
	ListServices(ctx context.Context) ([]swarm.Service, error)
	InspectService(ctx context.Context, id string) (swarm.Service, error)
	UpdateService(ctx context.Context, id string, version swarm.Version, spec swarm.ServiceSpec, registryAuth string) error
	RollbackService(ctx context.Context, id string, version swarm.Version, spec swarm.ServiceSpec) error
	ListServiceTasks(ctx context.Context, serviceID string) ([]swarm.Task, error)
	ListNodes(ctx context.Context) ([]swarm.Node, error)

	Close() error
}

// ImageSummary represents a Docker image for listing.
type ImageSummary struct {
	ID       string
	RepoTags []string
	Size     int64
	Created  int64
	InUse    bool
}

// ImagePruneResult summarises a prune operation.
type ImagePruneResult struct {
	ImagesDeleted  int
	SpaceReclaimed int64
}

// Verify Client implements API at compile time.
var _ API = (*Client)(nil)
