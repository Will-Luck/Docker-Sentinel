package docker

import (
	"context"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

func (c *Client) IsSwarmManager(ctx context.Context) bool {
	result, err := c.api.Info(ctx, client.InfoOptions{})
	if err != nil {
		return false
	}
	return result.Info.Swarm.LocalNodeState == swarm.LocalNodeStateActive &&
		result.Info.Swarm.ControlAvailable
}

func (c *Client) ListServices(ctx context.Context) ([]swarm.Service, error) {
	result, err := c.api.ServiceList(ctx, client.ServiceListOptions{Status: true})
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (c *Client) InspectService(ctx context.Context, id string) (swarm.Service, error) {
	result, err := c.api.ServiceInspect(ctx, id, client.ServiceInspectOptions{})
	if err != nil {
		return swarm.Service{}, err
	}
	return result.Service, nil
}

// UpdateService updates a Swarm service's spec. The version must be the current
// version from InspectService — stale versions cause a conflict error.
func (c *Client) UpdateService(ctx context.Context, id string, version swarm.Version, spec swarm.ServiceSpec, registryAuth string) error {
	_, err := c.api.ServiceUpdate(ctx, id, client.ServiceUpdateOptions{
		Version:             version,
		Spec:                spec,
		EncodedRegistryAuth: registryAuth,
	})
	return err
}

// RollbackService triggers Swarm's native rollback to the previous spec.
func (c *Client) RollbackService(ctx context.Context, id string, version swarm.Version) error {
	_, err := c.api.ServiceUpdate(ctx, id, client.ServiceUpdateOptions{
		Version:  version,
		Rollback: "previous",
	})
	return err
}

func (c *Client) ListNodes(ctx context.Context) ([]swarm.Node, error) {
	result, err := c.api.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (c *Client) ListServiceTasks(ctx context.Context, serviceID string) ([]swarm.Task, error) {
	f := client.Filters{}
	f = f.Add("service", serviceID)
	f = f.Add("desired-state", "running")
	result, err := c.api.TaskList(ctx, client.TaskListOptions{Filters: f})
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}
