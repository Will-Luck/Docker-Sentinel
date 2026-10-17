package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
)

// ghcrProbes replaces the updater's GHCR probe with one that records each
// image it is asked about and answers with err, or with an available
// alternative when err is nil.
func ghcrProbes(u *Updater, err error) func() []string {
	var mu sync.Mutex
	var probed []string
	u.ghcrAlternative = func(_ context.Context, image string, _, _ *registry.RegistryCredential) (*registry.GHCRAlternative, error) {
		mu.Lock()
		probed = append(probed, image)
		mu.Unlock()
		if err != nil {
			return nil, err
		}
		return &registry.GHCRAlternative{DockerHubImage: registry.RepoPath(image), GHCRImage: "ghcr.io/" + registry.RepoPath(image), Tag: registry.ExtractTag(image), Available: true}, nil
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), probed...)
	}
}

// The GHCR alternative check covers the images of containers on connected
// agent hosts, not only the hub's own. An image pinned by digest is left out:
// its GHCR "alternative" would be the :latest tag, another version.
func TestCheckGHCRAlternatives_IncludesAgentHostImages(t *testing.T) {
	u, _ := newTestUpdater(t, newMockDocker())
	u.ghcrCache = registry.NewGHCRCache(time.Hour)
	u.SetClusterScanner(&fakeCluster{
		connected: []string{"h1"},
		lists: map[string][]RemoteContainer{"h1": {
			{Name: "web", Image: "acme/web:1.0"},
			{Name: "local-twin", Image: "acme/hubonly:2.0"},
			{Name: "elsewhere", Image: "quay.io/acme/other:1"},
			{Name: "task", Image: "acme/pinned:1.0@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
			{Name: "bare", Image: "acme/bare@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		}},
	})
	probed := ghcrProbes(u, nil)

	u.checkGHCRAlternatives(context.Background(), []container.Summary{
		{Image: "acme/hubonly:2.0"},
		{Image: "acme/localpin@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	})

	want := map[string]bool{"acme/hubonly:2.0": true, "acme/web:1.0": true}
	got := probed()
	if len(got) != len(want) {
		t.Fatalf("probed %v, want exactly %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected probe of %q", p)
		}
	}
	if _, ok := u.ghcrCache.Get("acme/web", "1.0"); !ok {
		t.Errorf("the agent host's image has no cache entry")
	}
	for _, repo := range []string{"acme/pinned", "acme/bare", "acme/localpin"} {
		if _, ok := u.ghcrCache.Get(repo, "latest"); ok {
			t.Errorf("a digest-pinned image of %s left a :latest cache entry", repo)
		}
	}
}

// A failed probe leaves no cache entry, so without its own record of what it
// has tried, the check would probe an image once per host that runs it.
func TestCheckGHCRAlternatives_ProbesEachImageOnceWhenProbesFail(t *testing.T) {
	u, _ := newTestUpdater(t, newMockDocker())
	u.ghcrCache = registry.NewGHCRCache(time.Hour)
	u.SetClusterScanner(&fakeCluster{
		connected: []string{"h1", "h2"},
		lists: map[string][]RemoteContainer{
			"h1": {{Name: "web", Image: "acme/web:1.0"}},
			"h2": {{Name: "web", Image: "docker.io/acme/web:1.0"}, {Name: "web2", Image: "acme/web:1.0"}},
		},
	})
	probed := ghcrProbes(u, errors.New("registry unreachable"))

	u.checkGHCRAlternatives(context.Background(), []container.Summary{{Image: "acme/web:1.0"}})

	if got := probed(); len(got) != 1 {
		t.Errorf("probed %v, want acme/web:1.0 once", got)
	}
}

// clusterTurnedOff turns cluster mode off while it lists its first host, as
// the Settings page can while a GHCR check is running.
type clusterTurnedOff struct {
	*fakeCluster
	u    *Updater
	once sync.Once
}

func (c *clusterTurnedOff) ListContainers(ctx context.Context, hostID string) ([]RemoteContainer, error) {
	c.once.Do(func() { c.u.SetClusterScanner(nil) })
	return c.fakeCluster.ListContainers(ctx, hostID)
}

// Turning cluster mode off during the check must not crash it: the check
// keeps the scanner it started with for every host.
func TestCheckGHCRAlternatives_ClusterTurnedOffMidCheck(t *testing.T) {
	u, _ := newTestUpdater(t, newMockDocker())
	u.ghcrCache = registry.NewGHCRCache(time.Hour)
	u.SetClusterScanner(&clusterTurnedOff{u: u, fakeCluster: &fakeCluster{
		connected: []string{"h1", "h2"},
		listErr:   map[string]error{"h1": errors.New("agent gone")},
		lists:     map[string][]RemoteContainer{"h2": {{Name: "web", Image: "acme/web:1.0"}}},
	}})
	probed := ghcrProbes(u, nil)

	u.checkGHCRAlternatives(context.Background(), nil)

	if got := probed(); len(got) != 1 || got[0] != "acme/web:1.0" {
		t.Errorf("probed %v, want the second host's acme/web:1.0", got)
	}
}
