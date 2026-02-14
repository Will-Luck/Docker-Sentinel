package registry

import (
	"context"
	"fmt"
	"net/http"
)

// ProbeRateLimit makes a lightweight authenticated request to a registry to
// discover its rate limit headers. For Docker Hub, it fetches a token and
// performs a manifest HEAD on library/alpine:latest. For other registries,
// it makes a GET /v2/ request with Basic auth.
//
// Returns the response headers (which may or may not contain rate limit
// information depending on the registry).
func ProbeRateLimit(ctx context.Context, host string, cred *RegistryCredential) (http.Header, error) {
	host = NormaliseRegistryHost(host)

	if host == "docker.io" || host == "" {
		// Docker Hub: get a token, then HEAD a public manifest.
		token, err := FetchToken(ctx, "library/alpine", cred, host)
		if err != nil {
			return nil, fmt.Errorf("probe auth: %w", err)
		}
		_, headers, err := ManifestDigest(ctx, "library/alpine", "latest", token, host, cred)
		if err != nil {
			return nil, fmt.Errorf("probe manifest: %w", err)
		}
		return headers, nil
	}

	// Non-Docker Hub: GET /v2/ with Basic auth.
	// This is the lightest authenticated endpoint and many registries
	// return rate limit headers on it.
	url := "https://" + host + "/v2/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create probe request: %w", err)
	}
	if cred != nil {
		req.SetBasicAuth(cred.Username, cred.Secret)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe request: %w", err)
	}
	defer resp.Body.Close()

	return resp.Header, nil
}
