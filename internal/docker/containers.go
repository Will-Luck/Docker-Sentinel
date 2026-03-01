package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
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

// ImageID returns the image ID (sha256:...) for a given image reference.
func (c *Client) ImageID(ctx context.Context, imageRef string) (string, error) {
	resp, err := c.api.ImageInspect(ctx, imageRef)
	if err != nil {
		return "", err
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

// RemoveImage removes an image by ID, pruning untagged children.
func (c *Client) RemoveImage(ctx context.Context, id string) error {
	_, err := c.api.ImageRemove(ctx, id, client.ImageRemoveOptions{PruneChildren: true})
	return err
}

// TagImage applies a new tag to an existing image.
func (c *Client) TagImage(ctx context.Context, src, target string) error {
	_, err := c.api.ImageTag(ctx, client.ImageTagOptions{Source: src, Target: target})
	return err
}

// RemoveContainerWithVolumes removes a container (force) and its anonymous volumes.
func (c *Client) RemoveContainerWithVolumes(ctx context.Context, id string) error {
	_, err := c.api.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	return err
}

// ExecContainer runs a command inside a container and returns exit code + output.
func (c *Client) ExecContainer(ctx context.Context, id string, cmd []string, timeout int) (int, string, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
	}
	execCfg := client.ExecCreateOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	}
	execResp, err := c.api.ExecCreate(ctx, id, execCfg)
	if err != nil {
		return -1, "", fmt.Errorf("exec create: %w", err)
	}

	attachResp, err := c.api.ExecAttach(ctx, execResp.ID, client.ExecAttachOptions{})
	if err != nil {
		return -1, "", fmt.Errorf("exec attach: %w", err)
	}
	defer attachResp.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader); err != nil {
		return -1, "", fmt.Errorf("exec read: %w", err)
	}
	if stderr.Len() > 0 {
		stdout.WriteString(stderr.String())
	}
	buf := stdout

	inspectResp, err := c.api.ExecInspect(ctx, execResp.ID, client.ExecInspectOptions{})
	if err != nil {
		return -1, buf.String(), fmt.Errorf("exec inspect: %w", err)
	}

	return inspectResp.ExitCode, buf.String(), nil
}

// ContainerLogs returns the last N lines of a container's logs.
func (c *Client) ContainerLogs(ctx context.Context, id string, lines int) (string, error) {
	opts := client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", lines),
	}
	reader, err := c.api.ContainerLogs(ctx, id, opts)
	if err != nil {
		return "", fmt.Errorf("container logs: %w", err)
	}
	defer reader.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, reader); err != nil {
		// Some containers use raw TTY mode — fall back to direct read.
		reader2, err2 := c.api.ContainerLogs(ctx, id, opts)
		if err2 != nil {
			return "", fmt.Errorf("container logs fallback: %w", err2)
		}
		defer reader2.Close()
		raw, _ := io.ReadAll(reader2)
		return string(raw), nil
	}

	// Merge stdout and stderr.
	if stderr.Len() > 0 {
		stdout.WriteString(stderr.String())
	}
	return stdout.String(), nil
}
