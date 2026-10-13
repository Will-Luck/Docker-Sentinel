package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// Two releases of an app image whose defaults change between them.
var (
	appV1 = &container.Config{
		Env:     []string{"PATH=/usr/local/bin:/usr/bin", "APP_VERSION=1"},
		Cmd:     []string{"serve", "--v1"},
		Labels:  map[string]string{"org.opencontainers.image.version": "1"},
		Volumes: map[string]struct{}{"/data": {}},
	}
	appV2 = &container.Config{
		Env:     []string{"PATH=/usr/local/bin:/usr/bin", "APP_VERSION=2"},
		Cmd:     []string{"serve", "--v2"},
		Labels:  map[string]string{"org.opencontainers.image.version": "2"},
		Volumes: map[string]struct{}{"/data": {}},
	}
)

// fidelityHost has app:1 locally and app:2 in the registry.
func fidelityHost(t *testing.T) *fakeHost {
	t.Helper()
	h := newFakeHost()
	h.addImage("app:1", "sha256:app-1", appV1)
	h.registry["app:2"] = "sha256:app-2"
	h.imageConfigs["sha256:app-2"] = appV2
	return h
}

// webOnV1 is a container the user created from app:1 with their own env
// and label, on a user network with an alias.
func webOnV1(t *testing.T, h *fakeHost, hostCfg *container.HostConfig) *fakeContainer {
	t.Helper()
	if hostCfg == nil {
		hostCfg = &container.HostConfig{NetworkMode: "app_net"}
	}
	return h.addFromImage(t, "web", "app:1",
		&container.Config{Env: []string{"APP_MODE=prod"}, Labels: map[string]string{"com.example.team": "ops"}},
		hostCfg,
		&network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{"app_net": {Aliases: []string{"web"}}}},
		true)
}

func hasEnv(env []string, e string) bool { return slices.Contains(env, e) }

// live-1: after an update the container runs with the NEW image's command,
// environment and labels, keeps what the user set, and does not inherit
// the old container's hostname (its short ID) or short-ID alias.
func TestRecreateContainer_TakesTheNewImagesDefaults(t *testing.T) {
	h := fidelityHost(t)
	old := webOnV1(t, h, nil)
	a := newTestAgent(t.TempDir(), h)

	if _, err := a.recreateContainer(context.Background(), "web", "app:2"); err != nil {
		t.Fatalf("recreateContainer: %v", err)
	}
	c := h.byName("web")
	if !slices.Equal(c.cfg.Cmd, appV2.Cmd) {
		t.Errorf("cmd = %v, want the new image's %v", c.cfg.Cmd, appV2.Cmd)
	}
	if !hasEnv(c.env, "APP_VERSION=2") || hasEnv(c.env, "APP_VERSION=1") || !hasEnv(c.env, "APP_MODE=prod") {
		t.Errorf("env = %v, want APP_VERSION=2 and the user's APP_MODE=prod", c.env)
	}
	if c.labels["org.opencontainers.image.version"] != "2" || c.labels["com.example.team"] != "ops" {
		t.Errorf("labels = %v, want the new image's version and the user's team label", c.labels)
	}
	if c.cfg.Hostname == old.id[:12] {
		t.Errorf("hostname is still the old container's short ID %s", c.cfg.Hostname)
	}
	if aliases := c.networks["app_net"].Aliases; slices.Contains(aliases, old.id[:12]) || !slices.Contains(aliases, "web") {
		t.Errorf("app_net aliases = %v, want web without the old short ID %s", aliases, old.id[:12])
	}
}

// live-2: an anonymous volume (the image's VOLUME /data) holds the data.
// The replacement mounts that same volume, and removing the old container
// keeps it.
func TestRecreateContainer_CarriesAnonymousVolumes(t *testing.T) {
	h := fidelityHost(t)
	old := webOnV1(t, h, nil)
	vol := old.volumeAt("/data")
	if vol == "" {
		t.Fatal("test setup: no anonymous volume at /data")
	}
	a := newTestAgent(t.TempDir(), h)

	if _, err := a.recreateContainer(context.Background(), "web", "app:2"); err != nil {
		t.Fatalf("recreateContainer: %v", err)
	}
	if got := h.byName("web").volumeAt("/data"); got != vol {
		t.Errorf("/data is volume %q, want the old container's %q", got, vol)
	}
	if len(h.removedWithVolumes) != 0 {
		t.Errorf("old container removed with its volumes: %v", h.removedWithVolumes)
	}
}

// With remove_volumes (the container label, or the agent's own setting)
// the replacement starts on fresh anonymous volumes and the old ones go
// with the old container, as `docker compose up -V` does.
func TestRecreateContainer_FreshAnonymousVolumesWhenAsked(t *testing.T) {
	for _, tc := range []struct {
		name    string
		label   bool
		setting bool
	}{{"label", true, false}, {"agent setting", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := fidelityHost(t)
			labels := map[string]string{}
			if tc.label {
				labels["sentinel.remove-volumes"] = "true"
			}
			old := h.addFromImage(t, "web", "app:1", &container.Config{Labels: labels}, &container.HostConfig{}, nil, true)
			vol := old.volumeAt("/data")
			a := newTestAgent(t.TempDir(), h)
			a.cfg.RemoveVolumes = tc.setting

			if _, err := a.recreateContainer(context.Background(), "web", "app:2"); err != nil {
				t.Fatalf("recreateContainer: %v", err)
			}
			if got := h.byName("web").volumeAt("/data"); got == "" || got == vol {
				t.Errorf("/data is volume %q, want a fresh one (old was %q)", got, vol)
			}
			if len(h.removedWithVolumes) != 1 {
				t.Errorf("old container's volumes not removed with it: %v", h.removedWithVolumes)
			}
		})
	}
}

// A container in another container's network stack (a VPN sidecar
// consumer) gets the provider's hostname and the image's EXPOSE in its
// inspect; the daemon refuses both on create in that mode.
func TestRecreateContainer_ContainerNetworkMode(t *testing.T) {
	h := fidelityHost(t)
	h.imageConfigs["sha256:app-1"].ExposedPorts = network.PortSet{network.MustParsePort("8080/tcp"): {}}
	old := h.addFromImage(t, "web", "app:1", nil, &container.HostConfig{NetworkMode: "container:vpn"}, nil, true)
	old.cfg.Hostname = "vpn-host" // copied from the provider on start
	a := newTestAgent(t.TempDir(), h)

	if _, err := a.recreateContainer(context.Background(), "web", "app:2"); err != nil {
		t.Fatalf("recreateContainer of a container:vpn container: %v", err)
	}
	h.requireOnly(t, "web", "app:2", true)
	if mode := h.byName("web").hostCfg.NetworkMode; mode != "container:vpn" {
		t.Errorf("network mode = %q, want container:vpn", mode)
	}
}

// A container on the default bridge and a user network comes back on both,
// with its alias on the user network and only its own short ID.
func TestRecreateContainer_DefaultBridgeAndUserNetwork(t *testing.T) {
	h := fidelityHost(t)
	old := webOnV1(t, h, &container.HostConfig{NetworkMode: "bridge"})
	a := newTestAgent(t.TempDir(), h)

	if _, err := a.recreateContainer(context.Background(), "web", "app:2"); err != nil {
		t.Fatalf("recreateContainer: %v", err)
	}
	c := h.byName("web")
	if _, ok := c.networks["bridge"]; !ok {
		t.Error("replacement not on the default bridge")
	}
	aliases := c.networks["app_net"].Aliases
	if !slices.Contains(aliases, "web") || slices.Contains(aliases, old.id[:12]) || !slices.Contains(aliases, c.id[:12]) {
		t.Errorf("app_net aliases = %v, want web and the new short ID only", aliases)
	}
}

// The agent's own update builds the replacement the same way.
func TestSelfUpdateContainer_TakesTheNewImagesDefaults(t *testing.T) {
	h := fidelityHost(t)
	old := h.addFromImage(t, "sentinel-agent", "app:1",
		&container.Config{Labels: map[string]string{"sentinel.self": "true"}}, &container.HostConfig{}, nil, true)
	vol := old.volumeAt("/data")
	a := newTestAgent(t.TempDir(), h)

	if _, err := a.selfUpdateContainer(context.Background(), "sentinel-agent", "app:2"); err != nil {
		t.Fatalf("selfUpdateContainer: %v", err)
	}
	c := h.byName("sentinel-agent")
	if !hasEnv(c.env, "APP_VERSION=2") || hasEnv(c.env, "APP_VERSION=1") || c.labels["sentinel.self"] != "true" {
		t.Errorf("new agent env %v labels %v, want the new image's version and sentinel.self", c.env, c.labels)
	}
	if got := c.volumeAt("/data"); got != vol {
		t.Errorf("new agent /data is %q, want the old agent's %q (its enrolment lives there)", got, vol)
	}
}

// Bringing back a --rm container after a failed update recreates it as it
// was: nothing dropped as an image default, and on its own anonymous
// volumes.
func TestRecreateContainer_RestoredAutoRemoveContainerIsAsItWas(t *testing.T) {
	h := fidelityHost(t)
	old := webOnV1(t, h, &container.HostConfig{NetworkMode: "app_net", AutoRemove: true})
	old.autoRemove = true
	vol := old.volumeAt("/data")
	h.startErr["app:2"] = errors.New("port is already allocated")
	a := newTestAgent(t.TempDir(), h)

	if _, err := a.recreateContainer(context.Background(), "web", "app:2"); err == nil {
		t.Fatal("recreateContainer returned no error although start failed")
	}
	c := h.byName("web")
	if c == nil || !c.running {
		t.Fatalf("web not back and running: %+v", c)
	}
	if !hasEnv(c.request.cfg.Env, "APP_VERSION=1") || !slices.Equal(c.request.cfg.Cmd, appV1.Cmd) {
		t.Errorf("restore request env %v cmd %v, want the old container's as they were", c.request.cfg.Env, c.request.cfg.Cmd)
	}
	if got := c.volumeAt("/data"); got != vol {
		t.Errorf("restored /data is %q, want the old container's %q", got, vol)
	}
}
