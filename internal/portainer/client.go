package portainer

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func NewClient(baseURL, token string) *Client {
	// Always skip TLS verification for Portainer connections.
	// Portainer instances commonly use self-signed certs, especially
	// in homelab and private network setups.
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // self-signed Portainer certs
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}
}

func (c *Client) TestConnection(ctx context.Context) error {
	_, err := c.ListEndpoints(ctx)
	return err
}

func (c *Client) ListEndpoints(ctx context.Context) ([]Endpoint, error) {
	var endpoints []Endpoint
	if err := c.get(ctx, "/api/endpoints", &endpoints); err != nil {
		return nil, fmt.Errorf("list endpoints: %w", err)
	}
	return endpoints, nil
}

func (c *Client) ListContainers(ctx context.Context, endpointID int) ([]Container, error) {
	var containers []Container
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/json?all=1", endpointID)
	if err := c.get(ctx, path, &containers); err != nil {
		return nil, fmt.Errorf("list containers (endpoint %d): %w", endpointID, err)
	}
	return containers, nil
}

func (c *Client) ListStacks(ctx context.Context) ([]Stack, error) {
	var stacks []Stack
	if err := c.get(ctx, "/api/stacks", &stacks); err != nil {
		return nil, fmt.Errorf("list stacks: %w", err)
	}
	return stacks, nil
}

func (c *Client) RedeployStack(ctx context.Context, stackID, endpointID int, env []EnvVar) error {
	body := StackRedeploy{Env: env, PullImage: true, Prune: false}
	path := fmt.Sprintf("/api/stacks/%d?endpointId=%d", stackID, endpointID)
	return c.put(ctx, path, body)
}

func (c *Client) InspectContainer(ctx context.Context, endpointID int, containerID string) (*InspectResponse, error) {
	var resp InspectResponse
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/%s/json", endpointID, containerID)
	if err := c.get(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("inspect container: %w", err)
	}
	return &resp, nil
}

// InspectImage returns image metadata via Portainer's Docker proxy.
// The imageID should be the full sha256 image ID from the container list.
func (c *Client) InspectImage(ctx context.Context, endpointID int, imageID string) (*ImageInspect, error) {
	var resp ImageInspect
	path := fmt.Sprintf("/api/endpoints/%d/docker/images/%s/json", endpointID, url.PathEscape(imageID))
	if err := c.get(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("inspect image: %w", err)
	}
	return &resp, nil
}

func (c *Client) StopContainer(ctx context.Context, endpointID int, containerID string) error {
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/%s/stop", endpointID, containerID)
	return c.post(ctx, path, nil)
}

func (c *Client) RemoveContainer(ctx context.Context, endpointID int, containerID string) error {
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/%s", endpointID, containerID)
	return c.delete(ctx, path)
}

func (c *Client) PullImage(ctx context.Context, endpointID int, image, tag string) error {
	path := fmt.Sprintf("/api/endpoints/%d/docker/images/create?fromImage=%s&tag=%s", endpointID, url.QueryEscape(image), url.QueryEscape(tag))
	return c.post(ctx, path, nil)
}

func (c *Client) CreateContainer(ctx context.Context, endpointID int, name string, body interface{}) (string, error) {
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/create?name=%s", endpointID, url.QueryEscape(name))
	var resp ContainerCreateResponse
	if err := c.postJSON(ctx, path, body, &resp); err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	return resp.ID, nil
}

func (c *Client) StartContainer(ctx context.Context, endpointID int, containerID string) error {
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/%s/start", endpointID, containerID)
	return c.post(ctx, path, nil)
}

// WaitContainer blocks until the container exits and returns its exit code.
func (c *Client) WaitContainer(ctx context.Context, endpointID int, containerID string) (int, error) {
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/%s/wait", endpointID, containerID)
	var resp struct {
		StatusCode int `json:"StatusCode"`
	}
	if err := c.postJSON(ctx, path, nil, &resp); err != nil {
		return -1, err
	}
	return resp.StatusCode, nil
}

func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.token)
	return c.do(req, out)
}

func (c *Client) post(ctx context.Context, path string, body interface{}) error {
	return c.postJSON(ctx, path, body, nil)
}

func (c *Client) postJSON(ctx context.Context, path string, body interface{}, out interface{}) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req, out)
}

func (c *Client) put(ctx context.Context, path string, body interface{}) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.token)
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, nil)
}

func (c *Client) delete(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.token)
	return c.do(req, nil)
}

// WaitForRecovery polls the Portainer API until it responds successfully
// or the timeout expires. Used after updating Portainer itself, since the
// API goes down during the container recreation.
func (c *Client) WaitForRecovery(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("portainer did not recover within %v", timeout)
			}
			checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.TestConnection(checkCtx)
			cancel()
			if err == nil {
				return nil
			}
		}
	}
}

func (c *Client) do(req *http.Request, out interface{}) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Limit response body reads to 1MB to prevent memory exhaustion from
	// oversized or malicious responses.
	const maxBody = 1 << 20 // 1MB
	limited := io.LimitReader(resp.Body, maxBody)

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(limited)
		return fmt.Errorf("portainer API error %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if out != nil {
		return json.NewDecoder(limited).Decode(out)
	}
	return nil
}
