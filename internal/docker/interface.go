package docker

import (
	"context"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
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
	DistributionDigest(ctx context.Context, imageRef string) (string, error)
	RemoveImage(ctx context.Context, id string) error
	ExecContainer(ctx context.Context, id string, cmd []string, timeout int) (int, string, error)
	Close() error
}

// Verify Client implements API at compile time.
var _ API = (*Client)(nil)
