package imageconfig

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/moby/moby/api/types/container"
)

const configBlob = `{"architecture":"amd64","os":"linux","config":{"Env":["APP_VERSION=1.0.0"],"Cmd":["start-v1.0.0"],"Labels":{"v":"1.0.0"}}}`

// fakeRegistry serves one repository, lh/mut, holding a config blob, an
// image manifest pointing at it and an index pointing at the manifest. With
// token set it wants that Bearer token, issued by its /token endpoint.
type fakeRegistry struct {
	srv   *httptest.Server
	token string
	gets  atomic.Int32
}

func newFakeRegistry(t *testing.T, token string) *fakeRegistry {
	t.Helper()
	r := &fakeRegistry{token: token}
	manifest := `{"schemaVersion":2,"config":{"digest":"sha256:cfg"}}`
	index := fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"digest":"sha256:other","platform":{"os":"linux","architecture":"s390x"}},`+
		`{"digest":"sha256:man","platform":{"os":%q,"architecture":%q}}]}`, runtime.GOOS, runtime.GOARCH)
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/token" {
			if req.URL.Query().Get("scope") != "repository:lh/mut:pull" {
				http.Error(w, "bad scope", http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"token":%q}`, r.token)
			return
		}
		r.gets.Add(1)
		if r.token != "" && req.Header.Get("Authorization") != "Bearer "+r.token {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="repository:lh/mut:pull"`, r.srv.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch req.URL.Path {
		case "/v2/lh/mut/blobs/sha256:cfg":
			fmt.Fprint(w, configBlob)
		case "/v2/lh/mut/manifests/sha256:man":
			fmt.Fprint(w, manifest)
		case "/v2/lh/mut/manifests/sha256:idx":
			fmt.Fprint(w, index)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *fakeRegistry) fetcher(local *fakeLocal) *Fetcher {
	f := New(nil, func() []registry.RegistryCredential { return nil })
	if local != nil {
		f.local = local
	}
	f.client = r.srv.Client()
	f.baseURL = func(string) string { return r.srv.URL }
	return f
}

type fakeLocal struct{ configs map[string]*container.Config }

func (l *fakeLocal) ImageConfig(_ context.Context, ref string) (*container.Config, error) {
	if c, ok := l.configs[ref]; ok {
		return c, nil
	}
	return nil, errors.New("no such image")
}

func checkV100(t *testing.T, cfg *container.Config, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if !slices.Equal(cfg.Env, []string{"APP_VERSION=1.0.0"}) || !slices.Equal(cfg.Cmd, []string{"start-v1.0.0"}) || cfg.Labels["v"] != "1.0.0" {
		t.Errorf("config = %+v", cfg)
	}
}

func TestConfigFromRegistry(t *testing.T) {
	for _, tc := range []struct{ name, digest string }{
		{"image ID is the config blob", "sha256:cfg"},
		{"repository digest of a manifest", "reg.example/lh/mut@sha256:man"},
		{"repository digest of an index", "reg.example/lh/mut@sha256:idx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeRegistry(t, "")
			cfg, err := r.fetcher(nil).Config(context.Background(), Image{Ref: "reg.example/lh/mut:edge", Digest: tc.digest})
			checkV100(t, cfg, err)
		})
	}
}

// Registries that answer with a Bearer challenge (Docker Hub, GHCR,
// lscr.io, GitLab) are given the token their realm issues.
func TestConfigAnswersBearerChallenge(t *testing.T) {
	r := newFakeRegistry(t, "t0k")
	cfg, err := r.fetcher(nil).Config(context.Background(), Image{Ref: "reg.example/lh/mut:edge", Digest: "sha256:cfg"})
	checkV100(t, cfg, err)
}

// The host's copy is used when there is one, and a config is fetched once.
func TestConfigPrefersHostAndCaches(t *testing.T) {
	r := newFakeRegistry(t, "")
	local := &fakeLocal{configs: map[string]*container.Config{"sha256:local": {Env: []string{"A=1"}}}}
	f := r.fetcher(local)

	cfg, err := f.Config(context.Background(), Image{Ref: "reg.example/lh/mut:edge", Digest: "sha256:local"})
	if err != nil || !slices.Equal(cfg.Env, []string{"A=1"}) || r.gets.Load() != 0 {
		t.Fatalf("config = %+v err = %v registry gets = %d, want the host's copy", cfg, err, r.gets.Load())
	}
	for range 2 {
		cfg, err = f.Config(context.Background(), Image{Ref: "reg.example/lh/mut:edge", Digest: "sha256:cfg"})
	}
	checkV100(t, cfg, err)
	if n := r.gets.Load(); n != 1 {
		t.Errorf("registry gets = %d, want 1 (cached)", n)
	}
}

func TestConfigReportsWhatCannotBeFound(t *testing.T) {
	r := newFakeRegistry(t, "")
	f := r.fetcher(nil)
	for _, img := range []Image{
		{Ref: "reg.example/lh/mut:edge", Digest: "sha256:gone"},
		{Ref: "", Digest: "sha256:cfg"},
		{Ref: "reg.example/lh/mut:edge", Digest: ""},
	} {
		if cfg, err := f.Config(context.Background(), img); err == nil {
			t.Errorf("%+v: config = %+v, want an error", img, cfg)
		}
	}
}

func TestParseChallenge(t *testing.T) {
	got := parseChallenge(`realm="https://auth.example/token",service="registry.example",scope="repository:a/b:pull,push"`)
	if got["realm"] != "https://auth.example/token" || got["service"] != "registry.example" || got["scope"] != "repository:a/b:pull,push" {
		t.Errorf("parseChallenge = %v", got)
	}
}
