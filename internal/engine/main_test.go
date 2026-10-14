package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker/imageconfig"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
)

// noNetwork is an http.RoundTripper that refuses every request to a host
// that is not on this machine, at once, and remembers it. Engine tests must
// never reach the network: on a CI runner without internet a real registry
// or DNS lookup waits for its timeout, and the package timed out.
type noNetwork struct {
	next http.RoundTripper

	mu      sync.Mutex
	blocked map[string]bool
}

func (n *noNetwork) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return n.next.RoundTrip(req)
	}
	n.mu.Lock()
	n.blocked[req.URL.Scheme+"://"+req.URL.Host+req.URL.Path] = true
	n.mu.Unlock()
	return nil, fmt.Errorf("engine tests must not reach the network: %s %s", req.Method, req.URL)
}

// TestMain blocks outbound HTTP for every test in the package (the registry,
// image config and notification clients use the default transport) and
// fails the run if any test tried, naming the hosts.
func TestMain(m *testing.M) {
	guard := &noNetwork{next: http.DefaultTransport, blocked: make(map[string]bool)}
	http.DefaultTransport = guard
	code := m.Run()
	if len(guard.blocked) > 0 {
		hosts := make([]string, 0, len(guard.blocked))
		for h := range guard.blocked {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		fmt.Fprintf(os.Stderr, "FAIL: engine tests tried to reach the network: %v\n", hosts)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// errOffline is what the test stand-ins for registry lookups return.
var errOffline = errors.New("registry lookups are off in tests")

// offline keeps an Updater's registry lookups off the network. The digest
// comparison still runs against the Docker test double, as the real
// checker does, but no tags, versions, tokens or rate limits are fetched,
// and image configs come from the host only. Every test Updater
// constructor applies it.
func offline(u *Updater) *Updater {
	u.checkImage = func(ctx context.Context, imageRef string, _ docker.SemverScope, _, _ string) registry.CheckResult {
		return u.checker.Check(ctx, imageRef)
	}
	u.checkDigest = func(ctx context.Context, imageRef, digest string, _ docker.SemverScope, _, _ string) registry.CheckResult {
		result := registry.CheckResult{ImageRef: imageRef, LocalDigest: digest}
		if docker.IsLocalImage(imageRef) {
			result.IsLocal = true
			return result
		}
		remote, err := u.docker.DistributionDigest(ctx, imageRef)
		if err != nil {
			result.Error = err
			return result
		}
		result.RemoteDigest = remote
		hash := func(d string) string {
			if i := strings.LastIndex(d, "sha256:"); i >= 0 {
				return d[i:]
			}
			return d
		}
		result.UpdateAvailable = hash(digest) != hash(remote)
		return result
	}
	u.probeRateLimit = func(context.Context, string, *registry.RegistryCredential) (http.Header, error) {
		return nil, errOffline
	}
	u.ghcrAlternative = func(context.Context, string, *registry.RegistryCredential, *registry.RegistryCredential) (*registry.GHCRAlternative, error) {
		return nil, errOffline
	}
	local, _ := u.docker.(docker.ImageConfigReader)
	u.imageConfigs = imageconfig.NewLocal(local)
	return u
}
