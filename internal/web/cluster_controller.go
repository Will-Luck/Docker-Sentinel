package web

import (
	"context"
	"fmt"
	"sync"
)

// ClusterController is a thread-safe proxy for a ClusterProvider.
// It holds a stable pointer that the web server references — the underlying
// provider can be swapped at runtime (e.g. when cluster mode is toggled)
// without replacing the Dependencies struct.
type ClusterController struct {
	mu       sync.RWMutex
	provider ClusterProvider
}

// NewClusterController returns a ClusterController with no active provider.
// All methods return zero values until SetProvider is called.
func NewClusterController() *ClusterController {
	return &ClusterController{}
}

// SetProvider swaps the active ClusterProvider. Pass nil to disable clustering.
func (c *ClusterController) SetProvider(p ClusterProvider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.provider = p
}

// Enabled returns true if a provider is currently active.
func (c *ClusterController) Enabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.provider != nil
}

// AllHosts returns info about all registered agent hosts.
// Returns nil when clustering is disabled.
func (c *ClusterController) AllHosts() []ClusterHost {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return nil
	}
	return c.provider.AllHosts()
}

// GetHost returns info about a specific host.
// Returns zero value and false when clustering is disabled.
func (c *ClusterController) GetHost(id string) (ClusterHost, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return ClusterHost{}, false
	}
	return c.provider.GetHost(id)
}

// ConnectedHosts returns the IDs of currently connected agents.
// Returns nil when clustering is disabled.
func (c *ClusterController) ConnectedHosts() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return nil
	}
	return c.provider.ConnectedHosts()
}

// GenerateEnrollToken creates a new one-time enrollment token.
// Returns an error when clustering is disabled.
func (c *ClusterController) GenerateEnrollToken() (string, string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return "", "", fmt.Errorf("cluster not enabled")
	}
	return c.provider.GenerateEnrollToken()
}

// RemoveHost removes a host from the cluster.
// Returns an error when clustering is disabled.
func (c *ClusterController) RemoveHost(id string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return fmt.Errorf("cluster not enabled")
	}
	return c.provider.RemoveHost(id)
}

// RevokeHost revokes a host's certificate and removes it.
// Returns an error when clustering is disabled.
func (c *ClusterController) RevokeHost(id string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return fmt.Errorf("cluster not enabled")
	}
	return c.provider.RevokeHost(id)
}

// PauseHost sets a host to paused state (no new updates).
// Returns an error when clustering is disabled.
func (c *ClusterController) PauseHost(id string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return fmt.Errorf("cluster not enabled")
	}
	return c.provider.PauseHost(id)
}

// AllHostContainers returns containers from all connected hosts.
// Returns nil when clustering is disabled.
func (c *ClusterController) AllHostContainers() []RemoteContainer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return nil
	}
	return c.provider.AllHostContainers()
}

// UpdateRemoteContainer dispatches a container update to a remote agent.
// Returns an error when clustering is disabled.
func (c *ClusterController) UpdateRemoteContainer(ctx context.Context, hostID, containerName, targetImage, targetDigest string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return fmt.Errorf("cluster not enabled")
	}
	return c.provider.UpdateRemoteContainer(ctx, hostID, containerName, targetImage, targetDigest)
}

// RemoteContainerAction dispatches a lifecycle action to a container on a remote agent.
// Returns an error when clustering is disabled.
func (c *ClusterController) RemoteContainerAction(ctx context.Context, hostID, containerName, action string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return fmt.Errorf("cluster not enabled")
	}
	return c.provider.RemoteContainerAction(ctx, hostID, containerName, action)
}
