package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// httpClient is the shared HTTP client with a 10-second timeout for all
// registry auth requests.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// TokenResponse holds the bearer token returned by a registry auth endpoint.
type TokenResponse struct {
	Token string `json:"token"`
}

// FetchAnonymousToken retrieves an anonymous bearer token from Docker Hub's
// auth endpoint for the given repository (e.g. "library/nginx").
func FetchAnonymousToken(ctx context.Context, repo string) (string, error) {
	return FetchToken(ctx, repo, nil)
}

// FetchToken retrieves a bearer token from Docker Hub's auth endpoint.
// When cred is non-nil, Basic auth is included for higher rate limits.
func FetchToken(ctx context.Context, repo string, cred *RegistryCredential) (string, error) {
	url := "https://auth.docker.io/token?service=registry.docker.io&scope=repository:" + repo + ":pull"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create auth request: %w", err)
	}

	if cred != nil {
		req.SetBasicAuth(cred.Username, cred.Secret)
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
