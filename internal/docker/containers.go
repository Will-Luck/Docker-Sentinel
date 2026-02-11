package docker

import (
	"context"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// ListContainers returns all running containers.
func (c *Client) ListContainers(ctx context.Context) ([]container.Summary, error) {
	opts := client.ContainerListOptions{
		Filters: make(client.Filters).Add("status", "running"),
	}
	result, err := c.api.ContainerList(ctx, opts)
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

// ListAllContainers returns all containers regardless of state.
func (c *Client) ListAllContainers(ctx context.Context) ([]container.Summary, error) {
	result, err := c.api.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

// InspectContainer returns full container details by ID.
func (c *Client) InspectContainer(ctx context.Context, id string) (container.InspectResponse, error) {
	result, err := c.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, err
	}
	return result.Container, nil
}

// StopContainer stops a running container with the given timeout in seconds.
func (c *Client) StopContainer(ctx context.Context, id string, timeout int) error {
	_, err := c.api.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout})
	return err
}

// RemoveContainer removes a container (force).
func (c *Client) RemoveContainer(ctx context.Context, id string) error {
	_, err := c.api.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	return err
}

// CreateContainer creates a new container and returns its ID.
func (c *Client) CreateContainer(ctx context.Context, name string, cfg *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig) (string, error) {
	resp, err := c.api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:             name,
		Config:           cfg,
		HostConfig:       hostCfg,
		NetworkingConfig: netCfg,
	})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// StartContainer starts a stopped container.
func (c *Client) StartContainer(ctx context.Context, id string) error {
	_, err := c.api.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}

// RestartContainer restarts a running container.
func (c *Client) RestartContainer(ctx context.Context, id string) error {
	_, err := c.api.ContainerRestart(ctx, id, client.ContainerRestartOptions{})
	return err
}

// PullImage pulls an image by reference, waiting for pull to complete.
func (c *Client) PullImage(ctx context.Context, refStr string) error {
	resp, err := c.api.ImagePull(ctx, refStr, client.ImagePullOptions{})
	if err != nil {
		return err
	}
	return resp.Wait(ctx)
}

// ImageDigest returns the repo digest of a locally available image.
// Falls back to the image ID if no repo digest is available.
func (c *Client) ImageDigest(ctx context.Context, imageRef string) (string, error) {
	resp, err := c.api.ImageInspect(ctx, imageRef)
	if err != nil {
		return "", err
	}
	if len(resp.RepoDigests) > 0 {
		return resp.RepoDigests[0], nil
	}
	return resp.ID, nil
}

// DistributionDigest queries the registry for the current digest of an image
// reference, using the daemon's configured credentials.
func (c *Client) DistributionDigest(ctx context.Context, imageRef string) (string, error) {
	resp, err := c.api.DistributionInspect(ctx, imageRef, client.DistributionInspectOptions{})
	if err != nil {
		return "", err
	}
	return resp.Descriptor.Digest.String(), nil
}
