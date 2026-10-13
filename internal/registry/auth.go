package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"regexp"
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

// FetchAnonymousToken retrieves an anonymous bearer token from Docker Hub's
// auth endpoint for the given repository (e.g. "library/nginx").
func FetchAnonymousToken(ctx context.Context, repo string) (string, error) {
	return FetchToken(ctx, repo, nil, "docker.io")
}

// FetchToken retrieves a bearer token for the given registry.
// For Docker Hub (host "docker.io" or ""), it uses auth.docker.io.
// For GHCR, it uses ghcr.io/token.
// For other registries, returns empty — callers use Basic auth directly.
func FetchToken(ctx context.Context, repo string, cred *RegistryCredential, host string) (string, error) {
	if host == "ghcr.io" {
		return FetchGHCRToken(ctx, repo, cred)
	}
	// Cloud registry auth is handled by the caller via RegistryCredential.
	// The cloudauth package provides credentials that are passed in as cred.
	// Other registries use Basic auth directly on v2 API calls, unless they
	// answer with a Bearer challenge (lscr.io, registry.gitlab.com, ...).
	if host != "" && host != "docker.io" {
		return fetchChallengeToken(ctx, host, repo, cred)
	}

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

// fetchChallengeToken probes https://host/v2/. A registry that answers 401
// with a Bearer challenge names a token endpoint (realm) and service; the
// token is fetched from there for repo's pull scope, with the stored
// credential if there is one. A registry with no challenge, or a Basic one,
// needs no token and gets "".
func fetchChallengeToken(ctx context.Context, host, repo string, cred *RegistryCredential) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/v2/", nil)
	if err != nil {
		return "", fmt.Errorf("create registry probe: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("probe %s: %w", host, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return "", nil
	}
	realm, service, ok := parseBearerChallenge(resp.Header.Get("Www-Authenticate"))
	if !ok {
		return "", nil
	}

	u, err := neturl.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("bad token realm %q from %s: %w", realm, host, err)
	}
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	q.Set("scope", "repository:"+repo+":pull")
	u.RawQuery = q.Encode()

	treq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}
	if cred != nil {
		treq.SetBasicAuth(cred.Username, cred.Secret)
	}
	tresp, err := httpClient.Do(treq)
	if err != nil {
		return "", fmt.Errorf("fetch token from %s: %w", host, err)
	}
	defer tresp.Body.Close()
	if tresp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint of %s returned %d", host, tresp.StatusCode)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tresp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("decode token response from %s: %w", host, err)
	}
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	if tok.Token == "" {
		return "", fmt.Errorf("empty token from %s", host)
	}
	return tok.Token, nil
}

// bearerParamRE matches one key="value" parameter of an auth challenge.
var bearerParamRE = regexp.MustCompile(`(\w+)="([^"]*)"`)

// parseBearerChallenge reads the realm and service from a Www-Authenticate
// header value that starts with the Bearer scheme.
func parseBearerChallenge(header string) (realm, service string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(header), "Bearer ")
	if !found {
		return "", "", false
	}
	for _, m := range bearerParamRE.FindAllStringSubmatch(rest, -1) {
		switch strings.ToLower(m[1]) {
		case "realm":
			realm = m[2]
		case "service":
			service = m[2]
		}
	}
	return realm, service, realm != ""
}

// FetchGHCRToken retrieves a bearer token from GitHub Container Registry
// for the given repository (e.g. "go-gitea/gitea"). If cred is nil, an
// anonymous token is requested (sufficient for public images). If cred is
// provided, Basic auth is used on the token exchange request, granting
// access to private images.
func FetchGHCRToken(ctx context.Context, repo string, cred *RegistryCredential) (string, error) {
	url := "https://ghcr.io/token?service=ghcr.io&scope=repository:" + repo + ":pull"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create GHCR auth request: %w", err)
	}

	if cred != nil {
		req.SetBasicAuth(cred.Username, cred.Secret)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch GHCR token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GHCR auth endpoint returned %d", resp.StatusCode)
	}

	var tok TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("decode GHCR token response: %w", err)
	}
	if tok.Token == "" {
		return "", fmt.Errorf("empty token in GHCR response")
	}

	return tok.Token, nil
}
