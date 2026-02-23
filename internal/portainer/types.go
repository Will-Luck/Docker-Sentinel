package portainer

import (
	"encoding/json"
	"time"
)

// EndpointType mirrors Portainer's endpoint type enum.
type EndpointType int

const (
	EndpointDocker      EndpointType = 1 // Docker environment
	EndpointAgentDocker EndpointType = 2 // Agent on Docker
	EndpointAzure       EndpointType = 3
	EndpointEdgeAgent   EndpointType = 4
	EndpointKubernetes  EndpointType = 5
	EndpointEdgeK8s     EndpointType = 7
)

// EndpointStatus mirrors Portainer's endpoint status.
type EndpointStatus int

const (
	StatusUp   EndpointStatus = 1
	StatusDown EndpointStatus = 2
)

// Endpoint represents a Portainer-managed environment.
type Endpoint struct {
	ID     int            `json:"Id"`
	Name   string         `json:"Name"`
	URL    string         `json:"URL"`
	Type   EndpointType   `json:"Type"`
	Status EndpointStatus `json:"Status"`
}

// IsDocker returns true if this endpoint is a Docker environment we can scan.
func (e Endpoint) IsDocker() bool {
	return e.Type == EndpointDocker || e.Type == EndpointAgentDocker || e.Type == EndpointEdgeAgent
}

// StackType mirrors Portainer's stack type enum.
type StackType int

const (
	StackSwarm      StackType = 1
	StackCompose    StackType = 2
	StackKubernetes StackType = 3
)

// Stack represents a Portainer stack.
type Stack struct {
	ID         int       `json:"Id"`
	Name       string    `json:"Name"`
	Type       StackType `json:"Type"`
	EndpointID int       `json:"EndpointId"`
	Status     int       `json:"Status"` // 1 = active, 2 = inactive
	Env        []EnvVar  `json:"Env"`
}

// EnvVar is a key-value pair for stack environment variables.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Container is a simplified container from Portainer's Docker proxy.
type Container struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Labels  map[string]string `json:"Labels"`
	Created int64             `json:"Created"`
}

// Name returns the container name without the leading slash.
func (c Container) Name() string {
	if len(c.Names) == 0 {
		return ""
	}
	name := c.Names[0]
	if len(name) > 0 && name[0] == '/' {
		return name[1:]
	}
	return name
}

// StackName returns the compose project name from labels, or empty.
func (c Container) StackName() string {
	return c.Labels["com.docker.compose.project"]
}

// StackRedeploy is the request body for PUT /api/stacks/{id}.
type StackRedeploy struct {
	Env       []EnvVar `json:"env"`
	PullImage bool     `json:"pullImage"`
	Prune     bool     `json:"prune"`
}

// ImagePull is the query/body for POST /docker/images/create.
type ImagePull struct {
	FromImage string `json:"fromImage"`
	Tag       string `json:"tag"`
}

// ContainerCreateResponse is returned from POST /docker/containers/create.
type ContainerCreateResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

// InspectResponse is a minimal subset of container inspect data needed for recreation.
type InspectResponse struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Config struct {
		Image  string            `json:"Image"`
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig      json.RawMessage `json:"HostConfig"`
	Created         time.Time       `json:"Created"`
	NetworkSettings json.RawMessage `json:"NetworkSettings"`
}
