package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// httpClient is the shared HTTP client with a 10-second timeout for all
// registry auth requests.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// TokenResponse holds the bearer token returned by a registry auth endpoint.
type TokenResponse struct {
	Token string `json:"token"`
}

// AuthEntry holds decoded credentials from a Docker config file.
type AuthEntry struct {
	Username string
	Password string
}

// dockerConfig represents the top-level structure of ~/.docker/config.json.
type dockerConfig struct {
	Auths map[string]dockerConfigAuth `json:"auths"`
}

// dockerConfigAuth holds the base64-encoded "auth" field from config.json.
type dockerConfigAuth struct {
	Auth string `json:"auth"`
}

// FetchAnonymousToken retrieves an anonymous bearer token from Docker Hub's
// auth endpoint for the given repository (e.g. "library/nginx").
func FetchAnonymousToken(ctx context.Context, repo string) (string, error) {
	url := "https://auth.docker.io/token?service=registry.docker.io&scope=repository:" + repo + ":pull"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create auth request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth endpoint returned %d", resp.StatusCode)
	}

	var tok TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if tok.Token == "" {
		return "", fmt.Errorf("empty token in response")
	}

	return tok.Token, nil
}

// ReadDockerConfig parses a Docker config.json file and returns a map of
// registry hostname to decoded credentials. Each "auth" value is expected
// to be base64-encoded "username:password".
func ReadDockerConfig(path string) (map[string]AuthEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read docker config: %w", err)
	}

	var cfg dockerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse docker config: %w", err)
	}

	result := make(map[string]AuthEntry, len(cfg.Auths))
	for registry, auth := range cfg.Auths {
		if auth.Auth == "" {
			continue
		}

		decoded, err := base64.StdEncoding.DecodeString(auth.Auth)
		if err != nil {
			continue
		}

		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			continue
		}

		result[registry] = AuthEntry{
			Username: parts[0],
			Password: parts[1],
		}
	}

	return result, nil
}
