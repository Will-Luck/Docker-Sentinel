package portainer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// PortainerContainer is a container enriched with endpoint and stack membership.
type PortainerContainer struct {
	ID           string
	Name         string
	Image        string
	ImageID      string
	State        string
	Labels       map[string]string
	EndpointID   int
	EndpointName string
	StackID      int    // 0 if standalone
	StackName    string // "" if standalone
}

// HostID returns the logical host identifier for this container.
func (pc PortainerContainer) HostID() string {
	return fmt.Sprintf("portainer:%d", pc.EndpointID)
}

// Scanner wraps Client and provides higher-level scan operations.
type Scanner struct {
	client *Client

	mu     sync.Mutex
	stacks []Stack // cached for current scan cycle; nil means not yet fetched
}

// NewScanner returns a Scanner backed by the given client.
func NewScanner(client *Client) *Scanner {
	return &Scanner{client: client}
}

// Client returns the underlying Portainer client.
func (s *Scanner) Client() *Client {
	return s.client
}

// ResetCache clears the cached stack list. Call at the start of each scan cycle.
func (s *Scanner) ResetCache() {
	s.mu.Lock()
	s.stacks = nil
	s.mu.Unlock()
}

// Endpoints returns Docker endpoints that are currently up.
func (s *Scanner) Endpoints(ctx context.Context) ([]Endpoint, error) {
	all, err := s.client.ListEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	var out []Endpoint
	for _, ep := range all {
		if ep.IsDocker() && ep.Status == StatusUp {
			out = append(out, ep)
		}
	}
	return out, nil
}

// AllEndpoints returns all Docker endpoints regardless of status (for UI display).
func (s *Scanner) AllEndpoints(ctx context.Context) ([]Endpoint, error) {
	all, err := s.client.ListEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	var out []Endpoint
	for _, ep := range all {
		if ep.IsDocker() {
			out = append(out, ep)
		}
	}
	return out, nil
}

// EndpointContainers returns containers for the given endpoint, enriched with stack info.
// Stacks are fetched once per scan cycle and cached.
func (s *Scanner) EndpointContainers(ctx context.Context, ep Endpoint) ([]PortainerContainer, error) {
	stacks, err := s.cachedStacks(ctx)
	if err != nil {
		return nil, err
	}

	// Build map: project name -> Stack for this endpoint
	projectToStack := make(map[string]Stack)
	for _, st := range stacks {
		if st.EndpointID == ep.ID {
			projectToStack[st.Name] = st
		}
	}

	raw, err := s.client.ListContainers(ctx, ep.ID)
	if err != nil {
		return nil, err
	}

	out := make([]PortainerContainer, 0, len(raw))
	for _, c := range raw {
		pc := PortainerContainer{
			ID:           c.ID,
			Name:         c.Name(),
			Image:        c.Image,
			ImageID:      c.ImageID,
			State:        c.State,
			Labels:       c.Labels,
			EndpointID:   ep.ID,
			EndpointName: ep.Name,
		}
		if project := c.StackName(); project != "" {
			if st, ok := projectToStack[project]; ok {
				pc.StackID = st.ID
				pc.StackName = st.Name
			}
		}
		out = append(out, pc)
	}
	return out, nil
}

// RedeployStack triggers a stack redeploy, preserving the stack's existing env vars.
func (s *Scanner) RedeployStack(ctx context.Context, stackID, endpointID int) error {
	stacks, err := s.cachedStacks(ctx)
	if err != nil {
		return err
	}

	var env []EnvVar
	for _, st := range stacks {
		if st.ID == stackID {
			env = st.Env
			break
		}
	}

	return s.client.RedeployStack(ctx, stackID, endpointID, env)
}

// UpdateStandaloneContainer updates a standalone container: inspect -> stop -> remove ->
// pull new image -> create with same config -> start.
func (s *Scanner) UpdateStandaloneContainer(ctx context.Context, endpointID int, containerID, newImage string) error {
	insp, err := s.client.InspectContainer(ctx, endpointID, containerID)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}

	image, tag := parseImageTag(newImage)

	if err := s.client.StopContainer(ctx, endpointID, containerID); err != nil {
		return fmt.Errorf("stop: %w", err)
	}

	if err := s.client.RemoveContainer(ctx, endpointID, containerID); err != nil {
		return fmt.Errorf("remove: %w", err)
	}

	if err := s.client.PullImage(ctx, endpointID, image, tag); err != nil {
		return fmt.Errorf("pull: %w", err)
	}

	// Build create body preserving original config
	name := strings.TrimPrefix(insp.Name, "/")
	createBody := buildCreateBody(insp, newImage)

	newID, err := s.client.CreateContainer(ctx, endpointID, name, createBody)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	if err := s.client.StartContainer(ctx, endpointID, newID); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	return nil
}

// cachedStacks returns stacks from cache, fetching once per scan cycle.
func (s *Scanner) cachedStacks(ctx context.Context) ([]Stack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stacks != nil {
		return s.stacks, nil
	}

	stacks, err := s.client.ListStacks(ctx)
	if err != nil {
		return nil, err
	}
	s.stacks = stacks
	return s.stacks, nil
}

// parseImageTag splits an image reference into image and tag.
// Registry ports (e.g. registry.local:5000/myapp:v2) are handled correctly:
// only a colon that appears after the last slash is treated as a tag separator.
func parseImageTag(ref string) (image, tag string) {
	lastSlash := strings.LastIndex(ref, "/")
	afterSlash := ref[lastSlash+1:]

	colonIdx := strings.LastIndex(afterSlash, ":")
	if colonIdx < 0 {
		return ref, "latest"
	}

	// colon is at lastSlash+1+colonIdx in the original string
	splitAt := lastSlash + 1 + colonIdx
	return ref[:splitAt], ref[splitAt+1:]
}

// buildCreateBody assembles a minimal create-container request from an inspect response.
func buildCreateBody(insp *InspectResponse, newImage string) interface{} {
	type endpointSettings struct {
		Aliases    []string `json:"Aliases,omitempty"`
		NetworkID  string   `json:"NetworkID,omitempty"`
		MacAddress string   `json:"MacAddress,omitempty"`
	}
	type networkingConfig struct {
		EndpointsConfig map[string]*endpointSettings `json:"EndpointsConfig,omitempty"`
	}
	type createBody struct {
		Image            string            `json:"Image"`
		Env              []string          `json:"Env"`
		Labels           map[string]string `json:"Labels"`
		HostConfig       json.RawMessage   `json:"HostConfig,omitempty"`
		NetworkingConfig *networkingConfig `json:"NetworkingConfig,omitempty"`
	}

	// Extract network endpoints from NetworkSettings.Networks for the create call.
	var netCfg *networkingConfig
	if len(insp.NetworkSettings) > 0 {
		var ns struct {
			Networks map[string]*endpointSettings `json:"Networks"`
		}
		if err := json.Unmarshal(insp.NetworkSettings, &ns); err == nil && len(ns.Networks) > 0 {
			netCfg = &networkingConfig{EndpointsConfig: ns.Networks}
		}
	}

	return createBody{
		Image:            newImage,
		Env:              insp.Config.Env,
		Labels:           insp.Config.Labels,
		HostConfig:       insp.HostConfig,
		NetworkingConfig: netCfg,
	}
}
