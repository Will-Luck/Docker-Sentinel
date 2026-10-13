// Package imageconfig finds the config of an image a container ran, on the
// host or in its registry. Recreating a container needs the defaults of the
// images it ran to tell what it inherited from them (see
// docker.RecreateOptions.InheritedImages).
package imageconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/moby/moby/api/types/container"
)

// Image identifies an image a container ran: Digest is an image ID
// ("sha256:...") or a repository digest ("repo@sha256:..."), and Ref the
// reference the container ran it under, which names its repository.
type Image struct {
	Ref    string
	Digest string
}

// maxBody bounds a manifest or config read from a registry.
const maxBody = 4 << 20

// cacheSize bounds the cache; it is cleared when full.
const cacheSize = 256

const manifestAccept = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

// Fetcher finds image configs: from the host first, else from the
// registry with Sentinel's stored credentials. Configs are cached by
// digest, which names immutable content. It is safe for concurrent use.
type Fetcher struct {
	local  docker.ImageConfigReader
	creds  func() []registry.RegistryCredential
	client *http.Client
	// baseURL maps a registry host to its API base URL.
	baseURL func(host string) string

	mu    sync.Mutex
	cache map[string]*container.Config
}

// New returns a Fetcher that reads local images through local (nil to skip
// the host) and registries with the credentials creds returns (nil for
// none).
func New(local docker.ImageConfigReader, creds func() []registry.RegistryCredential) *Fetcher {
	return &Fetcher{
		local:   local,
		creds:   creds,
		client:  &http.Client{Timeout: 15 * time.Second},
		baseURL: registryBase,
		cache:   make(map[string]*container.Config),
	}
}

// NewLocal returns a Fetcher that only reads images on the host.
func NewLocal(local docker.ImageConfigReader) *Fetcher {
	f := New(local, nil)
	f.baseURL = nil
	return f
}

func registryBase(host string) string {
	if host == "" || host == "docker.io" {
		return "https://registry-1.docker.io"
	}
	return "https://" + host
}

// Config returns the config of img.
func (f *Fetcher) Config(ctx context.Context, img Image) (*container.Config, error) {
	if img.Digest == "" {
		return nil, errors.New("no digest")
	}
	f.mu.Lock()
	cfg, ok := f.cache[img.Digest]
	f.mu.Unlock()
	if ok {
		return cfg, nil
	}
	cfg, err := f.lookup(ctx, img)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	if len(f.cache) >= cacheSize {
		clear(f.cache)
	}
	f.cache[img.Digest] = cfg
	f.mu.Unlock()
	return cfg, nil
}

func (f *Fetcher) lookup(ctx context.Context, img Image) (*container.Config, error) {
	var localErr error
	if f.local != nil {
		cfg, err := f.local.ImageConfig(ctx, img.Digest)
		if err == nil {
			return cfg, nil
		}
		localErr = err
	}
	cfg, err := f.fromRegistry(ctx, img)
	if err != nil {
		return nil, errors.Join(localErr, err)
	}
	return cfg, nil
}

// fromRegistry reads the config from the image's registry. An image ID is,
// in the daemon's classic image store, the digest of the config blob; with
// the containerd store, and for a repository digest, it is a manifest (or
// index) digest.
func (f *Fetcher) fromRegistry(ctx context.Context, img Image) (*container.Config, error) {
	if f.baseURL == nil {
		return nil, errors.New("registry lookups are off")
	}
	ref, digest := img.Ref, img.Digest
	if repo, d, ok := strings.Cut(digest, "@"); ok {
		ref, digest = repo, d
	}
	if ref == "" || !strings.HasPrefix(digest, "sha256:") {
		return nil, fmt.Errorf("cannot look up %q in a registry", img.Digest)
	}
	host := registry.RegistryHost(ref)
	s := &session{f: f, base: f.baseURL(host) + "/v2/" + registry.RepoPath(ref)}
	if f.creds != nil {
		s.cred = registry.FindByRegistry(f.creds(), host)
	}

	if body, _, err := s.get(ctx, "/blobs/"+digest, ""); err == nil {
		if cfg, ok := parseConfig(body); ok {
			return cfg, nil
		}
	}
	body, err := s.manifest(ctx, digest)
	if err != nil {
		return nil, err
	}
	var m struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &m); err != nil || m.Config.Digest == "" {
		return nil, fmt.Errorf("manifest %s has no config", digest)
	}
	body, _, err = s.get(ctx, "/blobs/"+m.Config.Digest, "")
	if err != nil {
		return nil, err
	}
	cfg, ok := parseConfig(body)
	if !ok {
		return nil, fmt.Errorf("config %s cannot be read", m.Config.Digest)
	}
	return cfg, nil
}

// session is one lookup's requests to a repository, sharing a token.
type session struct {
	f     *Fetcher
	base  string
	cred  *registry.RegistryCredential
	token string
}

// manifest returns the image manifest for digest, choosing this platform's
// manifest when digest names an index.
func (s *session) manifest(ctx context.Context, digest string) ([]byte, error) {
	body, _, err := s.get(ctx, "/manifests/"+digest, manifestAccept)
	if err != nil {
		return nil, err
	}
	var idx struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform *struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if json.Unmarshal(body, &idx) != nil || len(idx.Manifests) == 0 {
		return body, nil
	}
	for _, m := range idx.Manifests {
		if m.Platform != nil && m.Platform.OS == runtime.GOOS && m.Platform.Architecture == runtime.GOARCH {
			body, _, err := s.get(ctx, "/manifests/"+m.Digest, manifestAccept)
			return body, err
		}
	}
	return nil, fmt.Errorf("index %s has no %s/%s manifest", digest, runtime.GOOS, runtime.GOARCH)
}

// get reads base+path, answering a Bearer challenge with a token and a
// Basic one with the stored credential.
func (s *session) get(ctx context.Context, path, accept string) ([]byte, string, error) {
	resp, err := s.do(ctx, s.base+path, accept)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		if err := s.authenticate(ctx, challenge); err != nil {
			return nil, "", err
		}
		if resp, err = s.do(ctx, s.base+path, accept); err != nil {
			return nil, "", err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return body, resp.Header.Get("Content-Type"), err
}

func (s *session) do(ctx context.Context, u, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	switch {
	case s.token != "":
		req.Header.Set("Authorization", "Bearer "+s.token)
	case s.cred != nil && s.cred.Username != "":
		req.SetBasicAuth(s.cred.Username, s.cred.Secret)
	}
	return s.f.client.Do(req)
}

// authenticate gets a token for a Bearer challenge.
func (s *session) authenticate(ctx context.Context, challenge string) error {
	scheme, params, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return errors.New("registry refused the request")
	}
	p := parseChallenge(params)
	if p["realm"] == "" {
		return errors.New("registry challenge has no realm")
	}
	q := url.Values{}
	if p["service"] != "" {
		q.Set("service", p["service"])
	}
	if p["scope"] != "" {
		q.Set("scope", p["scope"])
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p["realm"]+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if s.cred != nil && s.cred.Username != "" {
		req.SetBasicAuth(s.cred.Username, s.cred.Secret)
	}
	resp, err := s.f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry token: %s", resp.Status)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&tok); err != nil {
		return err
	}
	s.token = tok.Token
	if s.token == "" {
		s.token = tok.AccessToken
	}
	if s.token == "" {
		return errors.New("registry token is empty")
	}
	return nil
}

// parseChallenge reads the key="value" pairs of a WWW-Authenticate header.
func parseChallenge(s string) map[string]string {
	out := make(map[string]string)
	for s != "" {
		s = strings.TrimLeft(s, " ,")
		key, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		var val string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				break
			}
			val, s = rest[1:end+1], rest[end+2:]
		} else {
			val, s, _ = strings.Cut(rest, ",")
		}
		out[strings.ToLower(strings.TrimSpace(key))] = val
	}
	return out
}

// parseConfig reads an image config blob, telling it apart from a manifest
// (which has a schemaVersion and no os).
func parseConfig(body []byte) (*container.Config, bool) {
	var blob struct {
		SchemaVersion int               `json:"schemaVersion"`
		OS            string            `json:"os"`
		Config        *container.Config `json:"config"`
	}
	if json.Unmarshal(body, &blob) != nil || blob.SchemaVersion != 0 || blob.OS == "" {
		return nil, false
	}
	if blob.Config == nil {
		return &container.Config{}, true
	}
	return blob.Config, true
}
