package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client is an NPM API client that authenticates with email+password to obtain
// a JWT. Tokens are cached and refreshed automatically on 401.
type Client struct {
	baseURL  string
	email    string
	password string

	mu       sync.Mutex
	token    string
	tokenExp time.Time

	httpClient *http.Client
}

// NewClient returns an NPM client. Auth is deferred until the first request.
func NewClient(baseURL, email, password string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		email:      email,
		password:   password,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// TestConnection authenticates and returns any error.
func (c *Client) TestConnection(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authenticate(ctx)
}

// ListProxyHosts returns all configured proxy hosts.
func (c *Client) ListProxyHosts(ctx context.Context) ([]ProxyHost, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/nginx/proxy-hosts", nil)
	if err != nil {
		return nil, fmt.Errorf("list proxy hosts: %w", err)
	}
	defer resp.Body.Close()

	const maxBody = 2 << 20 // 2MB - proxy host lists can be large
	limited := io.LimitReader(resp.Body, maxBody)

	var hosts []ProxyHost
	if err := json.NewDecoder(limited).Decode(&hosts); err != nil {
		return nil, fmt.Errorf("decode proxy hosts: %w", err)
	}
	return hosts, nil
}

// authenticate obtains a JWT from NPM. Caller must hold c.mu.
func (c *Client) authenticate(ctx context.Context) error {
	body, err := json.Marshal(map[string]string{
		"identity": c.email,
		"secret":   c.password,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/tokens", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("npm auth request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("npm auth failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode auth response: %w", err)
	}
	if result.Token == "" {
		return fmt.Errorf("npm auth returned empty token")
	}

	c.token = result.Token
	// NPM tokens expire after 24h; refresh at 23h to avoid edge-case failures.
	c.tokenExp = time.Now().Add(23 * time.Hour)
	return nil
}

// validToken reports whether we have a non-expired token.
func (c *Client) validToken() bool {
	return c.token != "" && time.Now().Before(c.tokenExp)
}

// doRequest executes an HTTP request with automatic auth. On 401 it clears the
// token and retries once.
func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		c.mu.Lock()
		if !c.validToken() {
			if err := c.authenticate(ctx); err != nil {
				c.mu.Unlock()
				return nil, err
			}
		}
		tok := c.token
		c.mu.Unlock()

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			c.mu.Lock()
			// Only clear if the token hasn't been refreshed by another goroutine.
			if c.token == tok {
				c.token = ""
			}
			c.mu.Unlock()
			continue
		}

		if resp.StatusCode >= 400 {
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			return nil, fmt.Errorf("npm API error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}

		return resp, nil
	}
	return nil, fmt.Errorf("npm auth retry exhausted")
}
