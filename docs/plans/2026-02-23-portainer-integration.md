# Portainer Integration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Integrate with Portainer CE/BE to discover and update containers via Portainer's native API, with stack-aware redeploys and standalone container orchestration.

**Architecture:** Custom Portainer client in `internal/portainer/` speaking the Portainer API directly. `PortainerScanner` interface connects to the engine's scan loop. Two update paths: stack redeploy via `PUT /stacks/{id}` and standalone updates via Portainer's Docker proxy endpoints. Separate dashboard page at `/portainer`.

**Tech Stack:** Go, `net/http`, Portainer REST API, BoltDB settings store

**Design doc:** `docs/plans/2026-02-23-portainer-integration-design.md`

---

## Task 1: Portainer Types

Define all Portainer API response types.

**Files:**
- Create: `internal/portainer/types.go`

**Step 1: Write the types file**

```go
package portainer

import "time"

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
	Status     int       `json:"Status"`     // 1 = active, 2 = inactive
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

// ContainerCreateRequest is used for POST /docker/containers/create.
type ContainerCreateRequest struct {
	Image      string            `json:"Image"`
	Env        []string          `json:"Env,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
	HostConfig interface{}       `json:"HostConfig,omitempty"`
}

// ContainerCreateResponse is returned from POST /docker/containers/create.
type ContainerCreateResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

// InspectResponse is a minimal subset of container inspect data.
type InspectResponse struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Config struct {
		Image  string            `json:"Image"`
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig  interface{} `json:"HostConfig"`
	Created     time.Time   `json:"Created"`
	NetworkSettings interface{} `json:"NetworkSettings"`
}
```

**Step 2: Verify it compiles**

Run: `go build ./internal/portainer/...`
Expected: PASS (no errors)

**Step 3: Commit**

```
git add internal/portainer/types.go
git commit -m "feat(portainer): add Portainer API types"
```

---

## Task 2: Portainer HTTP Client

HTTP client that speaks Portainer's API. All methods operate against a single Portainer instance.

**Files:**
- Create: `internal/portainer/client.go`
- Create: `internal/portainer/client_test.go`

**Step 1: Write the test file**

Test against `httptest.Server` that returns canned JSON for each endpoint.

```go
package portainer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_ListEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "test-token" {
			w.WriteHeader(403)
			return
		}
		if r.URL.Path == "/api/endpoints" {
			json.NewEncoder(w).Encode([]Endpoint{
				{ID: 1, Name: "local", URL: "unix:///var/run/docker.sock", Type: EndpointDocker, Status: StatusUp},
				{ID: 2, Name: "remote", URL: "tcp://10.0.0.1:2375", Type: EndpointDocker, Status: StatusDown},
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	eps, err := c.ListEndpoints(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(eps))
	}
	if eps[0].Name != "local" {
		t.Errorf("expected first endpoint name 'local', got %q", eps[0].Name)
	}
}

func TestClient_ListContainers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/endpoints/1/docker/containers/json" && r.URL.Query().Get("all") == "1" {
			json.NewEncoder(w).Encode([]Container{
				{ID: "abc123", Names: []string{"/nginx"}, Image: "nginx:1.25", State: "running", Labels: map[string]string{}},
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	containers, err := c.ListContainers(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	if containers[0].Name() != "nginx" {
		t.Errorf("expected name 'nginx', got %q", containers[0].Name())
	}
}

func TestClient_ListStacks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/stacks" {
			json.NewEncoder(w).Encode([]Stack{
				{ID: 1, Name: "web", Type: StackCompose, EndpointID: 1, Status: 1},
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	stacks, err := c.ListStacks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 1 || stacks[0].Name != "web" {
		t.Fatalf("unexpected stacks: %+v", stacks)
	}
}

func TestClient_TestConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/endpoints" {
			json.NewEncoder(w).Encode([]Endpoint{})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	err := c.TestConnection(context.Background())
	if err != nil {
		t.Fatalf("expected successful connection test, got: %v", err)
	}
}

func TestClient_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "bad-token")
	_, err := c.ListEndpoints(context.Background())
	if err == nil {
		t.Fatal("expected auth error")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test -v -run TestClient ./internal/portainer/...`
Expected: FAIL (Client not defined)

**Step 3: Write the client implementation**

```go
package portainer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to a single Portainer instance via its REST API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient creates a Portainer API client.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// TestConnection verifies the API is reachable and the token is valid.
func (c *Client) TestConnection(ctx context.Context) error {
	_, err := c.ListEndpoints(ctx)
	return err
}

// ListEndpoints returns all Portainer endpoints/environments.
func (c *Client) ListEndpoints(ctx context.Context) ([]Endpoint, error) {
	var endpoints []Endpoint
	if err := c.get(ctx, "/api/endpoints", &endpoints); err != nil {
		return nil, fmt.Errorf("list endpoints: %w", err)
	}
	return endpoints, nil
}

// ListContainers lists containers on a specific endpoint via Portainer's Docker proxy.
func (c *Client) ListContainers(ctx context.Context, endpointID int) ([]Container, error) {
	var containers []Container
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/json?all=1", endpointID)
	if err := c.get(ctx, path, &containers); err != nil {
		return nil, fmt.Errorf("list containers (endpoint %d): %w", endpointID, err)
	}
	return containers, nil
}

// ListStacks returns all stacks across all endpoints.
func (c *Client) ListStacks(ctx context.Context) ([]Stack, error) {
	var stacks []Stack
	if err := c.get(ctx, "/api/stacks", &stacks); err != nil {
		return nil, fmt.Errorf("list stacks: %w", err)
	}
	return stacks, nil
}

// RedeployStack triggers a stack redeploy with image pull.
func (c *Client) RedeployStack(ctx context.Context, stackID, endpointID int, env []EnvVar) error {
	body := StackRedeploy{
		Env:       env,
		PullImage: true,
		Prune:     false,
	}
	path := fmt.Sprintf("/api/stacks/%d?endpointId=%d", stackID, endpointID)
	return c.put(ctx, path, body)
}

// InspectContainer returns detailed info about a container on an endpoint.
func (c *Client) InspectContainer(ctx context.Context, endpointID int, containerID string) (*InspectResponse, error) {
	var resp InspectResponse
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/%s/json", endpointID, containerID)
	if err := c.get(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("inspect container: %w", err)
	}
	return &resp, nil
}

// StopContainer stops a container on an endpoint.
func (c *Client) StopContainer(ctx context.Context, endpointID int, containerID string) error {
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/%s/stop", endpointID, containerID)
	return c.post(ctx, path, nil)
}

// RemoveContainer removes a container on an endpoint.
func (c *Client) RemoveContainer(ctx context.Context, endpointID int, containerID string) error {
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/%s", endpointID, containerID)
	return c.delete(ctx, path)
}

// PullImage pulls an image on an endpoint via Portainer's Docker proxy.
func (c *Client) PullImage(ctx context.Context, endpointID int, image, tag string) error {
	path := fmt.Sprintf("/api/endpoints/%d/docker/images/create?fromImage=%s&tag=%s", endpointID, image, tag)
	return c.post(ctx, path, nil)
}

// CreateContainer creates a container on an endpoint.
func (c *Client) CreateContainer(ctx context.Context, endpointID int, name string, body interface{}) (string, error) {
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/create?name=%s", endpointID, name)
	var resp ContainerCreateResponse
	if err := c.postJSON(ctx, path, body, &resp); err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	return resp.ID, nil
}

// StartContainer starts a container on an endpoint.
func (c *Client) StartContainer(ctx context.Context, endpointID int, containerID string) error {
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/%s/start", endpointID, containerID)
	return c.post(ctx, path, nil)
}

// get makes a GET request and decodes the JSON response.
func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("portainer API %s: %d %s", path, resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// post makes a POST request with an optional JSON body.
func (c *Client) post(ctx context.Context, path string, body interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = strings.NewReader(string(data))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("portainer API POST %s: %d %s", path, resp.StatusCode, string(b))
	}
	return nil
}

// postJSON makes a POST request with JSON body and decodes the response.
func (c *Client) postJSON(ctx context.Context, path string, body, out interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("portainer API POST %s: %d %s", path, resp.StatusCode, string(b))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// put makes a PUT request with a JSON body.
func (c *Client) put(ctx context.Context, path string, body interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+path, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("portainer API PUT %s: %d %s", path, resp.StatusCode, string(b))
	}
	return nil
}

// delete makes a DELETE request.
func (c *Client) delete(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("portainer API DELETE %s: %d %s", path, resp.StatusCode, string(b))
	}
	return nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test -v -run TestClient ./internal/portainer/...`
Expected: PASS

**Step 5: Commit**

```
git add internal/portainer/client.go internal/portainer/client_test.go
git commit -m "feat(portainer): add HTTP client for Portainer API"
```

---

## Task 3: Portainer Scanner

The scanner converts Portainer API data into engine types and handles the scan loop integration. This is the bridge between Portainer's API and the engine's existing container scanning logic.

**Files:**
- Create: `internal/portainer/scanner.go`
- Create: `internal/portainer/scanner_test.go`

**Step 1: Write the test file**

```go
package portainer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
)

func TestScanner_Endpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/endpoints":
			json.NewEncoder(w).Encode([]Endpoint{
				{ID: 1, Name: "local", URL: "unix:///var/run/docker.sock", Type: EndpointDocker, Status: StatusUp},
				{ID: 2, Name: "offline", URL: "tcp://10.0.0.1:2375", Type: EndpointDocker, Status: StatusDown},
				{ID: 3, Name: "k8s", URL: "https://k8s.local", Type: EndpointKubernetes, Status: StatusUp},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	s := NewScanner(NewClient(srv.URL, "tok"), nil)
	eps, err := s.Endpoints(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Should only return Docker endpoints that are up
	if len(eps) != 1 {
		t.Fatalf("expected 1 active Docker endpoint, got %d", len(eps))
	}
	if eps[0].Name != "local" {
		t.Errorf("expected endpoint 'local', got %q", eps[0].Name)
	}
}

func TestScanner_EndpointContainers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/endpoints/1/docker/containers/json":
			json.NewEncoder(w).Encode([]Container{
				{
					ID: "abc", Names: []string{"/nginx"}, Image: "nginx:1.25",
					State: "running", Labels: map[string]string{"com.docker.compose.project": "web"},
				},
				{
					ID: "def", Names: []string{"/redis"}, Image: "redis:7",
					State: "running", Labels: map[string]string{},
				},
			})
		case "/api/stacks":
			json.NewEncoder(w).Encode([]Stack{
				{ID: 10, Name: "web", Type: StackCompose, EndpointID: 1, Status: 1},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	s := NewScanner(NewClient(srv.URL, "tok"), nil)
	containers, err := s.EndpointContainers(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(containers))
	}

	// Check that stack info is attached
	var nginx *PortainerContainer
	for i, c := range containers {
		if c.Name == "nginx" {
			nginx = &containers[i]
		}
	}
	if nginx == nil {
		t.Fatal("nginx container not found")
	}
	if nginx.StackID != 10 {
		t.Errorf("expected stack ID 10, got %d", nginx.StackID)
	}
	if nginx.StackName != "web" {
		t.Errorf("expected stack name 'web', got %q", nginx.StackName)
	}
}

func TestScanner_ToRemoteContainer(t *testing.T) {
	pc := PortainerContainer{
		ID: "abc", Name: "nginx", Image: "nginx:1.25", State: "running",
		Labels: map[string]string{"foo": "bar"},
		EndpointID: 1, EndpointName: "local",
	}
	rc := pc.ToRemoteContainer()
	if rc.Name != "nginx" {
		t.Errorf("expected name 'nginx', got %q", rc.Name)
	}
	if rc.HostID != "portainer:1" {
		t.Errorf("expected hostID 'portainer:1', got %q", rc.HostID)
	}
	_ = rc // use engine.RemoteContainer type check
}
```

**Step 2: Run tests to verify they fail**

Run: `go test -v -run TestScanner ./internal/portainer/...`
Expected: FAIL

**Step 3: Write the scanner implementation**

```go
package portainer

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
)

// PortainerContainer is a container enriched with Portainer-specific metadata.
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

// ToRemoteContainer converts to the engine's RemoteContainer type.
// HostID uses the format "portainer:{endpointID}" to distinguish from cluster hosts.
func (pc PortainerContainer) ToRemoteContainer() engine.RemoteContainer {
	return engine.RemoteContainer{
		ID:     pc.ID,
		Name:   pc.Name,
		Image:  pc.Image,
		State:  pc.State,
		Labels: pc.Labels,
	}
}

// HostID returns the scoped host identifier for this container's endpoint.
func (pc PortainerContainer) HostID() string {
	return fmt.Sprintf("portainer:%d", pc.EndpointID)
}

// Scanner provides Portainer container discovery for the engine scan loop.
type Scanner struct {
	client *Client
	log    *slog.Logger
	stacks []Stack // cached per scan cycle
}

// NewScanner creates a Portainer scanner.
func NewScanner(client *Client, log *slog.Logger) *Scanner {
	if log == nil {
		log = slog.Default()
	}
	return &Scanner{client: client, log: log}
}

// Endpoints returns active Docker endpoints (Status=Up, Type=Docker).
func (s *Scanner) Endpoints(ctx context.Context) ([]Endpoint, error) {
	all, err := s.client.ListEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	var active []Endpoint
	for _, ep := range all {
		if ep.IsDocker() && ep.Status == StatusUp {
			active = append(active, ep)
		}
	}
	return active, nil
}

// AllEndpoints returns all Docker endpoints regardless of status (for UI).
func (s *Scanner) AllEndpoints(ctx context.Context) ([]Endpoint, error) {
	all, err := s.client.ListEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	var docker []Endpoint
	for _, ep := range all {
		if ep.IsDocker() {
			docker = append(docker, ep)
		}
	}
	return docker, nil
}

// EndpointContainers returns enriched containers for a specific endpoint,
// with stack membership resolved.
func (s *Scanner) EndpointContainers(ctx context.Context, endpointID int) ([]PortainerContainer, error) {
	// Fetch stacks if not already cached this cycle.
	if s.stacks == nil {
		stacks, err := s.client.ListStacks(ctx)
		if err != nil {
			s.log.Warn("failed to list Portainer stacks", "error", err)
			s.stacks = []Stack{} // don't retry this cycle
		} else {
			s.stacks = stacks
		}
	}

	// Build lookup: compose project name -> stack (for this endpoint).
	stackByProject := make(map[string]Stack)
	for _, st := range s.stacks {
		if st.EndpointID == endpointID && st.Type == StackCompose {
			stackByProject[st.Name] = st
		}
	}

	containers, err := s.client.ListContainers(ctx, endpointID)
	if err != nil {
		return nil, err
	}

	result := make([]PortainerContainer, 0, len(containers))
	for _, c := range containers {
		pc := PortainerContainer{
			ID:         c.ID,
			Name:       c.Name(),
			Image:      c.Image,
			ImageID:    c.ImageID,
			State:      c.State,
			Labels:     c.Labels,
			EndpointID: endpointID,
		}
		if project := c.StackName(); project != "" {
			if st, ok := stackByProject[project]; ok {
				pc.StackID = st.ID
				pc.StackName = st.Name
			}
		}
		result = append(result, pc)
	}
	return result, nil
}

// ResetCache clears the cached stacks (call at start of each scan cycle).
func (s *Scanner) ResetCache() {
	s.stacks = nil
}

// Client returns the underlying Portainer API client (for update operations).
func (s *Scanner) Client() *Client {
	return s.client
}
```

**Step 4: Run tests to verify they pass**

Run: `go test -v -run TestScanner ./internal/portainer/...`
Expected: PASS

**Step 5: Commit**

```
git add internal/portainer/scanner.go internal/portainer/scanner_test.go
git commit -m "feat(portainer): add scanner for endpoint/container discovery"
```

---

## Task 4: Config and Settings

Add Portainer URL/token to config and settings store.

**Files:**
- Modify: `internal/config/config.go` (add fields, env vars, getters)
- Modify: `internal/web/api_settings.go` (add Portainer settings handlers)
- Modify: `internal/web/server.go` (register routes, add `PortainerProvider` interface)

**Step 1: Add config fields**

In `internal/config/config.go`, add to the `Config` struct (after the cluster fields, ~line 62):

```go
	// Portainer integration
	PortainerURL   string
	PortainerToken string
```

In `Load()` (after `GracePeriodOffline` line ~141):

```go
		// Portainer integration
		PortainerURL:   envStr("SENTINEL_PORTAINER_URL", ""),
		PortainerToken: envStr("SENTINEL_PORTAINER_TOKEN", ""),
```

In `Values()` method, add (find it by searching for `func (c *Config) Values()`):

```go
	if c.PortainerURL != "" {
		m["SENTINEL_PORTAINER_URL"] = c.PortainerURL
	}
	// Token is intentionally NOT exposed in Values() - it's a secret.
```

**Step 2: Add settings handlers**

In `internal/web/api_settings.go`, add Portainer settings handlers:

```go
// apiSetPortainerURL saves the Portainer URL setting.
func (s *Server) apiSetPortainerURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.URL = strings.TrimRight(body.URL, "/")

	if s.deps.SettingsStore != nil {
		if err := s.deps.SettingsStore.SaveSetting("portainer_url", body.URL); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	s.logEvent(r, "settings", "", "Portainer URL changed")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiSetPortainerToken saves the Portainer API token.
func (s *Server) apiSetPortainerToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if s.deps.SettingsStore != nil {
		if err := s.deps.SettingsStore.SaveSetting("portainer_token", body.Token); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	s.logEvent(r, "settings", "", "Portainer token updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiTestPortainerConnection tests the Portainer connection.
func (s *Server) apiTestPortainerConnection(w http.ResponseWriter, r *http.Request) {
	if s.deps.Portainer == nil {
		writeError(w, http.StatusBadRequest, "Portainer not configured")
		return
	}
	if err := s.deps.Portainer.TestConnection(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
	})
}
```

**Step 3: Add PortainerProvider interface and routes**

In `internal/web/server.go`, add to `Dependencies` struct (~line 61, after `Cluster`):

```go
	Portainer PortainerProvider // nil when Portainer integration is not configured
```

Add the interface (after `ClusterProvider`):

```go
// PortainerProvider provides Portainer endpoint and container data for the web layer.
type PortainerProvider interface {
	TestConnection(ctx context.Context) error
	Endpoints(ctx context.Context) ([]PortainerEndpoint, error)
	AllEndpoints(ctx context.Context) ([]PortainerEndpoint, error)
	EndpointContainers(ctx context.Context, endpointID int) ([]PortainerContainerInfo, error)
}

// PortainerEndpoint is a Portainer endpoint for the web layer.
type PortainerEndpoint struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Status string `json:"status"` // "up" or "down"
}

// PortainerContainerInfo is a Portainer container for the web layer.
type PortainerContainerInfo struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	State        string            `json:"state"`
	Labels       map[string]string `json:"labels,omitempty"`
	EndpointID   int               `json:"endpoint_id"`
	EndpointName string            `json:"endpoint_name"`
	StackID      int               `json:"stack_id,omitempty"`
	StackName    string            `json:"stack_name,omitempty"`
}
```

Register routes in `registerRoutes()` (after the cluster routes block, ~line 747):

```go
	// Portainer
	s.mux.Handle("GET /portainer", perm(auth.PermSettingsModify, s.handlePortainer))
	s.mux.Handle("GET /api/portainer/endpoints", perm(auth.PermContainersView, s.apiPortainerEndpoints))
	s.mux.Handle("GET /api/portainer/endpoints/{id}/containers", perm(auth.PermContainersView, s.apiPortainerContainers))
	s.mux.Handle("POST /api/settings/portainer-url", perm(auth.PermSettingsModify, s.apiSetPortainerURL))
	s.mux.Handle("POST /api/settings/portainer-token", perm(auth.PermSettingsModify, s.apiSetPortainerToken))
	s.mux.Handle("POST /api/settings/portainer-test", perm(auth.PermSettingsModify, s.apiTestPortainerConnection))
```

**Step 4: Verify it compiles**

Run: `go build ./...`
Expected: PASS (some handler stubs may need empty implementations - see Task 5)

**Step 5: Commit**

```
git add internal/config/config.go internal/web/api_settings.go internal/web/server.go
git commit -m "feat(portainer): add config, settings handlers, and web interfaces"
```

---

## Task 5: Portainer Web API Handlers

REST endpoints for the Portainer dashboard page data.

**Files:**
- Create: `internal/web/api_portainer.go`

**Step 1: Write the handler file**

```go
package web

import (
	"net/http"
	"strconv"
)

// handlePortainer serves the Portainer dashboard page.
func (s *Server) handlePortainer(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "portainer.html", nil)
}

// apiPortainerEndpoints returns all Portainer endpoints.
func (s *Server) apiPortainerEndpoints(w http.ResponseWriter, r *http.Request) {
	if s.deps.Portainer == nil {
		writeJSON(w, http.StatusOK, []PortainerEndpoint{})
		return
	}
	endpoints, err := s.deps.Portainer.AllEndpoints(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if endpoints == nil {
		endpoints = []PortainerEndpoint{}
	}
	writeJSON(w, http.StatusOK, endpoints)
}

// apiPortainerContainers returns containers for a specific Portainer endpoint.
func (s *Server) apiPortainerContainers(w http.ResponseWriter, r *http.Request) {
	if s.deps.Portainer == nil {
		writeJSON(w, http.StatusOK, []PortainerContainerInfo{})
		return
	}
	idStr := r.PathValue("id")
	endpointID, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}
	containers, err := s.deps.Portainer.EndpointContainers(r.Context(), endpointID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if containers == nil {
		containers = []PortainerContainerInfo{}
	}
	writeJSON(w, http.StatusOK, containers)
}
```

**Step 2: Verify it compiles**

Run: `go build ./internal/web/...`
Expected: PASS

**Step 3: Commit**

```
git add internal/web/api_portainer.go
git commit -m "feat(portainer): add web API handlers for endpoints and containers"
```

---

## Task 6: Portainer Adapter and Wiring

Bridge the Portainer scanner to the web interfaces and wire everything up in main.go.

**Files:**
- Modify: `cmd/sentinel/adapters.go` (add `portainerAdapter`)
- Modify: `cmd/sentinel/main.go` (wire up Portainer client/scanner if configured)

**Step 1: Add the adapter**

In `cmd/sentinel/adapters.go`, add the Portainer adapter (after the cluster adapters):

```go
// portainerAdapter bridges portainer.Scanner to web.PortainerProvider.
type portainerAdapter struct {
	scanner *portainer.Scanner
}

func (a *portainerAdapter) TestConnection(ctx context.Context) error {
	return a.scanner.Client().TestConnection(ctx)
}

func (a *portainerAdapter) Endpoints(ctx context.Context) ([]web.PortainerEndpoint, error) {
	eps, err := a.scanner.Endpoints(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]web.PortainerEndpoint, len(eps))
	for i, ep := range eps {
		status := "up"
		if ep.Status == portainer.StatusDown {
			status = "down"
		}
		result[i] = web.PortainerEndpoint{
			ID:     ep.ID,
			Name:   ep.Name,
			URL:    ep.URL,
			Status: status,
		}
	}
	return result, nil
}

func (a *portainerAdapter) AllEndpoints(ctx context.Context) ([]web.PortainerEndpoint, error) {
	eps, err := a.scanner.AllEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]web.PortainerEndpoint, len(eps))
	for i, ep := range eps {
		status := "up"
		if ep.Status == portainer.StatusDown {
			status = "down"
		}
		result[i] = web.PortainerEndpoint{
			ID:     ep.ID,
			Name:   ep.Name,
			URL:    ep.URL,
			Status: status,
		}
	}
	return result, nil
}

func (a *portainerAdapter) EndpointContainers(ctx context.Context, endpointID int) ([]web.PortainerContainerInfo, error) {
	containers, err := a.scanner.EndpointContainers(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	result := make([]web.PortainerContainerInfo, len(containers))
	for i, c := range containers {
		result[i] = web.PortainerContainerInfo{
			ID:           c.ID,
			Name:         c.Name,
			Image:        c.Image,
			State:        c.State,
			Labels:       c.Labels,
			EndpointID:   c.EndpointID,
			EndpointName: c.EndpointName,
			StackID:      c.StackID,
			StackName:    c.StackName,
		}
	}
	return result, nil
}
```

**Step 2: Wire up in main.go**

In `cmd/sentinel/main.go`, after the cluster wiring block, add:

```go
	// Portainer integration (optional — enabled when URL and token are configured).
	portainerURL := cfg.PortainerURL
	portainerToken := cfg.PortainerToken
	// Also check runtime settings for URL/token.
	if portainerURL == "" {
		if v, err := db.LoadSetting("portainer_url"); err == nil && v != "" {
			portainerURL = v
		}
	}
	if portainerToken == "" {
		if v, err := db.LoadSetting("portainer_token"); err == nil && v != "" {
			portainerToken = v
		}
	}
	if portainerURL != "" && portainerToken != "" {
		pc := portainer.NewClient(portainerURL, portainerToken)
		ps := portainer.NewScanner(pc, log)
		updater.SetPortainerScanner(ps)
		webDeps.Portainer = &portainerAdapter{scanner: ps}
		log.Info("portainer integration enabled", "url", portainerURL)
	}
```

Add the import for `internal/portainer` to `main.go`'s import block.

**Step 3: Verify it compiles**

Run: `go build ./...`
Expected: Compile error - `updater.SetPortainerScanner` doesn't exist yet. That's Task 7.

**Step 4: Commit** (after Task 7 makes it compile)

Hold this commit - combine with Task 7.

---

## Task 7: Engine Integration

Add `SetPortainerScanner()` and `scanPortainerEndpoints()` to the engine updater. This is the core integration that makes Portainer containers appear in the scan loop.

**Files:**
- Modify: `internal/engine/updater.go` (add portainer field, setter, scan method)

**Step 1: Add the PortainerScanner interface**

In `internal/engine/updater.go`, add after the `ClusterScanner` interface (~line 86):

```go
// PortainerScanner provides access to Portainer-managed containers for scanning.
// Nil when Portainer integration is disabled.
type PortainerScanner interface {
	// Endpoints returns active Docker endpoints.
	Endpoints(ctx context.Context) ([]PortainerEndpointInfo, error)
	// EndpointContainers returns containers for a specific endpoint.
	EndpointContainers(ctx context.Context, endpointID int) ([]PortainerContainerResult, error)
	// ResetCache clears cached stacks (called at start of each scan).
	ResetCache()
	// RedeployStack triggers a stack redeploy with image pull.
	RedeployStack(ctx context.Context, stackID, endpointID int) error
	// UpdateStandaloneContainer orchestrates stop/remove/pull/create/start.
	UpdateStandaloneContainer(ctx context.Context, endpointID int, containerID, newImage string) error
}

// PortainerEndpointInfo is a minimal endpoint descriptor for the engine.
type PortainerEndpointInfo struct {
	ID   int
	Name string
}

// PortainerContainerResult is a container from Portainer with stack metadata.
type PortainerContainerResult struct {
	ID         string
	Name       string
	Image      string
	State      string
	Labels     map[string]string
	EndpointID int
	StackID    int
	StackName  string
}
```

**Step 2: Add the field and setter**

In the `Updater` struct, add after `haDiscovery` (~line 136):

```go
	portainer  PortainerScanner // optional: nil = no Portainer integration
```

Add setter after `SetHADiscovery`:

```go
// SetPortainerScanner attaches a Portainer scanner for endpoint container scanning.
func (u *Updater) SetPortainerScanner(ps PortainerScanner) {
	u.portainer = ps
}
```

**Step 3: Add scanPortainerEndpoints**

Add after `scanRemoteHost` method:

```go
// ---------------------------------------------------------------------------
// Portainer endpoint scanning
// ---------------------------------------------------------------------------

// scanPortainerEndpoints iterates Portainer endpoints and scans their containers.
// Registry checks run server-side (same as cluster scanning); update dispatch
// goes through Portainer's API instead of the Docker socket.
func (u *Updater) scanPortainerEndpoints(ctx context.Context, mode ScanMode, result *ScanResult, filters []string, reserve int) {
	u.portainer.ResetCache()

	endpoints, err := u.portainer.Endpoints(ctx)
	if err != nil {
		u.log.Warn("portainer: failed to list endpoints", "error", err)
		return
	}

	if len(endpoints) == 0 {
		return
	}

	u.log.Info("scanning portainer endpoints", "count", len(endpoints))

	for _, ep := range endpoints {
		if ctx.Err() != nil {
			return
		}
		u.scanPortainerEndpoint(ctx, ep, mode, result, filters, reserve)
	}
}

func (u *Updater) scanPortainerEndpoint(ctx context.Context, ep PortainerEndpointInfo, mode ScanMode, result *ScanResult, filters []string, reserve int) {
	containers, err := u.portainer.EndpointContainers(ctx, ep.ID)
	if err != nil {
		u.log.Error("portainer: failed to list containers", "endpoint", ep.Name, "error", err)
		return
	}

	u.log.Info("scanning portainer endpoint", "endpoint", ep.Name, "containers", len(containers))

	portainerDefault := u.cfg.DefaultPolicy()
	if u.settings != nil {
		if v, err := u.settings.LoadSetting("portainer_default_policy"); err == nil && v != "" {
			portainerDefault = v
		}
	}

	// Track which stacks have already been redeployed this cycle.
	redeployedStacks := make(map[int]bool)

	for _, c := range containers {
		if ctx.Err() != nil {
			return
		}

		result.Total++
		hostID := fmt.Sprintf("portainer:%d", c.EndpointID)

		tag := registry.ExtractTag(c.Image)
		resolved := ResolvePolicy(u.store, c.Labels, store.ScopedKey(hostID, c.Name), tag, portainerDefault, u.cfg.LatestAutoUpdate())
		policy := docker.Policy(resolved.Policy)

		if policy == docker.PolicyPinned {
			u.log.Debug("skipping pinned portainer container", "endpoint", ep.Name, "name", c.Name)
			result.Skipped++
			continue
		}

		if MatchesFilter(c.Name, filters) {
			u.log.Debug("skipping filtered portainer container", "endpoint", ep.Name, "name", c.Name)
			result.Skipped++
			continue
		}

		// Rate limit check.
		if u.rateTracker != nil {
			regHost := registry.RegistryHost(c.Image)
			canProceed, wait := u.rateTracker.CanProceed(regHost, reserve)
			if !canProceed {
				if mode == ScanManual {
					u.log.Warn("rate limit exhausted during portainer scan",
						"registry", regHost, "resets_in", wait)
					result.RateLimited++
					return
				}
				result.RateLimited++
				continue
			}
		}

		// Registry check.
		semverScope := docker.ContainerSemverScope(c.Labels)
		includeRE, excludeRE := docker.ContainerTagFilters(c.Labels)
		check := u.checker.CheckVersionedWithDigest(ctx, c.Image, "", semverScope, includeRE, excludeRE)

		if check.Error != nil {
			u.log.Warn("portainer registry check failed", "endpoint", ep.Name, "name", c.Name, "error", check.Error)
			continue
		}

		if !check.UpdateAvailable {
			continue
		}

		scanTarget := check.ResolvedTarget()
		scopedKey := store.ScopedKey(hostID, c.Name)

		switch policy {
		case docker.PolicyAuto:
			result.AutoCount++

			if u.isDryRun() {
				u.log.Info("dry-run: would update portainer container",
					"endpoint", ep.Name, "name", c.Name, "target", scanTarget)
				_ = u.store.RecordUpdate(store.UpdateRecord{
					Timestamp:     u.clock.Now(),
					ContainerName: c.Name,
					OldImage:      c.Image,
					NewImage:      scanTarget,
					Outcome:       "dry_run",
					Host:          ep.Name,
				})
				continue
			}

			// Stack containers: redeploy the stack (once per stack per cycle).
			if c.StackID > 0 {
				if redeployedStacks[c.StackID] {
					continue // already triggered this cycle
				}
				u.log.Info("portainer: redeploying stack",
					"endpoint", ep.Name, "stack", c.StackName, "trigger", c.Name)
				if err := u.portainer.RedeployStack(ctx, c.StackID, c.EndpointID); err != nil {
					u.log.Error("portainer: stack redeploy failed",
						"endpoint", ep.Name, "stack", c.StackName, "error", err)
					_ = u.store.RecordUpdate(store.UpdateRecord{
						Timestamp:     u.clock.Now(),
						ContainerName: c.Name,
						OldImage:      c.Image,
						NewImage:      scanTarget,
						Outcome:       "failed",
						Error:         err.Error(),
						Host:          ep.Name,
					})
					result.Failed++
					continue
				}
				redeployedStacks[c.StackID] = true
				_ = u.store.RecordUpdate(store.UpdateRecord{
					Timestamp:     u.clock.Now(),
					ContainerName: c.Name,
					OldImage:      c.Image,
					NewImage:      scanTarget,
					Outcome:       "success",
					Host:          ep.Name,
				})
				result.Updated++
				continue
			}

			// Standalone containers: orchestrate via Portainer's Docker proxy.
			u.log.Info("portainer: updating standalone container",
				"endpoint", ep.Name, "name", c.Name, "target", scanTarget)
			if err := u.portainer.UpdateStandaloneContainer(ctx, c.EndpointID, c.ID, scanTarget); err != nil {
				u.log.Error("portainer: standalone update failed",
					"endpoint", ep.Name, "name", c.Name, "error", err)
				_ = u.store.RecordUpdate(store.UpdateRecord{
					Timestamp:     u.clock.Now(),
					ContainerName: c.Name,
					OldImage:      c.Image,
					NewImage:      scanTarget,
					Outcome:       "failed",
					Error:         err.Error(),
					Host:          ep.Name,
				})
				result.Failed++
				continue
			}
			_ = u.store.RecordUpdate(store.UpdateRecord{
				Timestamp:     u.clock.Now(),
				ContainerName: c.Name,
				OldImage:      c.Image,
				NewImage:      scanTarget,
				Outcome:       "success",
				Host:          ep.Name,
			})
			result.Updated++

		case docker.PolicyManual:
			u.queue.Add(PendingUpdate{
				Key:                    scopedKey,
				ContainerName:          c.Name,
				CurrentImage:           c.Image,
				NewImage:               scanTarget,
				DetectedAt:             u.clock.Now(),
				Host:                   ep.Name,
				ResolvedCurrentVersion: check.ResolvedCurrentVersion,
				ResolvedTargetVersion:  check.ResolvedTargetVersion,
			})
			u.log.Info("portainer: update queued", "endpoint", ep.Name, "name", c.Name)
			u.publishEvent(events.EventQueueChange, c.Name, "queued for approval")
			result.Queued++
		}
	}
}
```

**Step 4: Call from Scan()**

In the `Scan()` method, after the cluster scanning block (~line 797, after `u.scanRemoteHosts`):

```go
	// Scan Portainer endpoints if integration is active.
	if u.portainer != nil {
		u.scanPortainerEndpoints(ctx, mode, &result, filters, reserve)
	}
```

**Step 5: Add scanner methods for update operations**

Back in `internal/portainer/scanner.go`, add update methods that implement the engine interface:

```go
// RedeployStack triggers a stack redeploy via Portainer's API.
func (s *Scanner) RedeployStack(ctx context.Context, stackID, endpointID int) error {
	// Find stack env vars to preserve them during redeploy.
	var env []EnvVar
	for _, st := range s.stacks {
		if st.ID == stackID {
			env = st.Env
			break
		}
	}
	return s.client.RedeployStack(ctx, stackID, endpointID, env)
}

// UpdateStandaloneContainer orchestrates: inspect -> stop -> remove -> pull -> create -> start.
func (s *Scanner) UpdateStandaloneContainer(ctx context.Context, endpointID int, containerID, newImage string) error {
	// Inspect to capture config for recreation.
	info, err := s.client.InspectContainer(ctx, endpointID, containerID)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}

	name := info.Name
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}

	// Parse image into repo and tag.
	image, tag := parseImageTag(newImage)

	// Stop.
	if err := s.client.StopContainer(ctx, endpointID, containerID); err != nil {
		return fmt.Errorf("stop: %w", err)
	}

	// Remove.
	if err := s.client.RemoveContainer(ctx, endpointID, containerID); err != nil {
		return fmt.Errorf("remove: %w", err)
	}

	// Pull new image.
	if err := s.client.PullImage(ctx, endpointID, image, tag); err != nil {
		return fmt.Errorf("pull: %w", err)
	}

	// Create with same config but new image.
	createBody := map[string]interface{}{
		"Image":           newImage,
		"Env":             info.Config.Env,
		"Labels":          info.Config.Labels,
		"HostConfig":      info.HostConfig,
		"NetworkingConfig": info.NetworkSettings,
	}
	newID, err := s.client.CreateContainer(ctx, endpointID, name, createBody)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	// Start.
	if err := s.client.StartContainer(ctx, endpointID, newID); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	return nil
}

// parseImageTag splits "nginx:1.25" into ("nginx", "1.25").
func parseImageTag(ref string) (string, string) {
	// Handle images with registry port (e.g. registry.local:5000/image:tag).
	lastColon := strings.LastIndex(ref, ":")
	lastSlash := strings.LastIndex(ref, "/")
	if lastColon > lastSlash && lastColon > 0 {
		return ref[:lastColon], ref[lastColon+1:]
	}
	return ref, "latest"
}
```

Add `"strings"` to the import block in scanner.go.

**Step 6: Add adapter for engine interface**

The scanner needs to implement `engine.PortainerScanner`. Add an adapter in `cmd/sentinel/adapters.go`:

```go
// portainerScannerAdapter bridges portainer.Scanner to engine.PortainerScanner.
type portainerScannerAdapter struct {
	scanner *portainer.Scanner
}

func (a *portainerScannerAdapter) Endpoints(ctx context.Context) ([]engine.PortainerEndpointInfo, error) {
	eps, err := a.scanner.Endpoints(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]engine.PortainerEndpointInfo, len(eps))
	for i, ep := range eps {
		result[i] = engine.PortainerEndpointInfo{ID: ep.ID, Name: ep.Name}
	}
	return result, nil
}

func (a *portainerScannerAdapter) EndpointContainers(ctx context.Context, endpointID int) ([]engine.PortainerContainerResult, error) {
	containers, err := a.scanner.EndpointContainers(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	result := make([]engine.PortainerContainerResult, len(containers))
	for i, c := range containers {
		result[i] = engine.PortainerContainerResult{
			ID:         c.ID,
			Name:       c.Name,
			Image:      c.Image,
			State:      c.State,
			Labels:     c.Labels,
			EndpointID: c.EndpointID,
			StackID:    c.StackID,
			StackName:  c.StackName,
		}
	}
	return result, nil
}

func (a *portainerScannerAdapter) ResetCache() {
	a.scanner.ResetCache()
}

func (a *portainerScannerAdapter) RedeployStack(ctx context.Context, stackID, endpointID int) error {
	return a.scanner.RedeployStack(ctx, stackID, endpointID)
}

func (a *portainerScannerAdapter) UpdateStandaloneContainer(ctx context.Context, endpointID int, containerID, newImage string) error {
	return a.scanner.UpdateStandaloneContainer(ctx, endpointID, containerID, newImage)
}
```

Update the main.go wiring to use this adapter:

```go
	if portainerURL != "" && portainerToken != "" {
		pc := portainer.NewClient(portainerURL, portainerToken)
		ps := portainer.NewScanner(pc, log)
		updater.SetPortainerScanner(&portainerScannerAdapter{scanner: ps})
		webDeps.Portainer = &portainerAdapter{scanner: ps}
		log.Info("portainer integration enabled", "url", portainerURL)
	}
```

**Step 7: Verify it compiles**

Run: `go build ./...`
Expected: PASS

**Step 8: Run all tests**

Run: `go test -count=1 ./...`
Expected: PASS

**Step 9: Commit** (includes deferred Task 6 commit)

```
git add internal/engine/updater.go internal/portainer/scanner.go cmd/sentinel/adapters.go cmd/sentinel/main.go
git commit -m "feat(portainer): integrate scanner into engine scan loop and wire up in main"
```

---

## Task 8: Portainer Dashboard Page

HTML page at `/portainer` showing endpoints and their containers.

**Files:**
- Create: `internal/web/static/portainer.html`
- Modify: `internal/web/static/index.html` (add nav link)
- Modify: `internal/web/static/settings.html` (add nav link)
- Modify: `internal/web/static/cluster.html` (add nav link)
- Modify: `internal/web/static/queue.html` (add nav link)
- Modify: `internal/web/static/history.html` (add nav link)
- Modify: `internal/web/static/logs.html` (add nav link)

**Step 1: Create the Portainer HTML page**

Follow the same template pattern as `cluster.html`. The page should:

- Use `{{define "portainer.html"}}` template syntax
- Include the same nav structure as other pages, with a Portainer link that is conditionally shown (like the Cluster link uses `{{if .ClusterEnabled}}`)
- Add a Portainer nav link: `{{if .PortainerEnabled}}<a href="/portainer" class="nav-link active" aria-current="page">Portainer</a>{{end}}`
- Show a connection status indicator
- List endpoints as expandable cards with status, URL, container count
- Within each endpoint, show a container table (same columns as dashboard: name, image, state, stack)
- Stack containers grouped under stack name headers
- JS fetches from `/api/portainer/endpoints` then `/api/portainer/endpoints/{id}/containers` for each

**Step 2: Add the PortainerEnabled template variable**

In `internal/web/server.go`, add `PortainerEnabled bool` to the template data struct (wherever `ClusterEnabled` is set). Set it to `s.deps.Portainer != nil`.

Find the `renderPage` or template data builder and add alongside `ClusterEnabled`:

```go
"PortainerEnabled": s.deps.Portainer != nil,
```

**Step 3: Add nav links to all pages**

In each HTML template, add after the Cluster nav link:

```html
{{if .PortainerEnabled}}<a href="/portainer" class="nav-link">Portainer</a>{{end}}
```

On `portainer.html` itself, make it `active`:

```html
{{if .PortainerEnabled}}<a href="/portainer" class="nav-link active" aria-current="page">Portainer</a>{{end}}
```

**Step 4: Add Portainer section to settings page**

In `settings.html`, add a Portainer configuration section with:
- URL input field
- Token input field (masked)
- Test connection button
- Enable/disable toggle (optional - just connecting is enough)

**Step 5: Verify build**

Run: `go build ./...`
Expected: PASS

**Step 6: Commit**

```
git add internal/web/static/portainer.html internal/web/static/index.html internal/web/static/settings.html internal/web/static/cluster.html internal/web/static/queue.html internal/web/static/history.html internal/web/static/logs.html internal/web/server.go
git commit -m "feat(portainer): add dashboard page and settings UI"
```

---

## Task 9: Integration Test and Polish

End-to-end verification that the full flow works.

**Files:**
- Create: `internal/portainer/integration_test.go`
- Modify: `internal/portainer/scanner_test.go` (add update operation tests)

**Step 1: Add scanner update tests**

```go
func TestScanner_RedeployStack(t *testing.T) {
	var redeployed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/stacks" && r.Method == "GET":
			json.NewEncoder(w).Encode([]Stack{
				{ID: 1, Name: "web", Type: StackCompose, EndpointID: 1, Status: 1, Env: []EnvVar{{Name: "FOO", Value: "bar"}}},
			})
		case r.URL.Path == "/api/stacks/1" && r.Method == "PUT":
			var body StackRedeploy
			json.NewDecoder(r.Body).Decode(&body)
			if !body.PullImage {
				t.Error("expected PullImage=true")
			}
			if len(body.Env) != 1 || body.Env[0].Name != "FOO" {
				t.Error("expected preserved env vars")
			}
			redeployed = true
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	s := NewScanner(NewClient(srv.URL, "tok"), nil)
	// Prime the stack cache.
	s.stacks = []Stack{
		{ID: 1, Name: "web", Type: StackCompose, EndpointID: 1, Status: 1, Env: []EnvVar{{Name: "FOO", Value: "bar"}}},
	}
	err := s.RedeployStack(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !redeployed {
		t.Error("stack was not redeployed")
	}
}

func TestParseImageTag(t *testing.T) {
	tests := []struct {
		input     string
		wantImage string
		wantTag   string
	}{
		{"nginx:1.25", "nginx", "1.25"},
		{"ghcr.io/user/app:v2.0", "ghcr.io/user/app", "v2.0"},
		{"registry.local:5000/myapp:latest", "registry.local:5000/myapp", "latest"},
		{"nginx", "nginx", "latest"},
	}
	for _, tt := range tests {
		img, tag := parseImageTag(tt.input)
		if img != tt.wantImage || tag != tt.wantTag {
			t.Errorf("parseImageTag(%q) = (%q, %q), want (%q, %q)",
				tt.input, img, tag, tt.wantImage, tt.wantTag)
		}
	}
}
```

**Step 2: Run all tests**

Run: `go test -v -count=1 ./internal/portainer/...`
Expected: PASS

**Step 3: Run full test suite**

Run: `go test -count=1 ./...`
Expected: PASS

**Step 4: Build and verify**

Run: `go build ./...`
Expected: PASS

**Step 5: Commit**

```
git add internal/portainer/scanner_test.go
git commit -m "test(portainer): add update operation and image parsing tests"
```

---

## Task 10: Deploy and Verify

Build, deploy to test server, and verify everything works.

**Step 1: Deploy**

Run: `make dev-deploy`

**Step 2: Verify endpoints**

Open `http://192.168.1.60:62850` and check:

- `/portainer` page loads (even without a Portainer instance configured, should show empty state)
- Settings page has Portainer section
- Nav bar shows "Portainer" link
- All existing pages still work (dashboard, queue, history, settings, cluster)
- Version footer shows the dev tag

**Step 3: Verify API endpoints**

```bash
curl http://192.168.1.60:62850/api/portainer/endpoints
# Should return [] (empty array - no Portainer configured on test instance)
```

**Step 4: Commit any fixes from verification**

If any issues found, fix and commit individually.

---

## Verification Checklist

After all tasks:
1. `go test -count=1 ./...` passes
2. `go build ./...` passes
3. Deployed to `.60:62850` and manually verified
4. New Portainer nav link appears in all pages
5. Settings page has Portainer URL/token fields
6. `/api/portainer/endpoints` returns valid JSON
7. No regressions in existing functionality
