package docker

import (
	"maps"
	"net/netip"
	"slices"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

func TestSanitiseForNetworkModeContainer(t *testing.T) {
	port := network.MustParsePort("80/tcp")
	cfg := &container.Config{
		Hostname:     "provider-host",
		Domainname:   "lan",
		ExposedPorts: network.PortSet{port: {}},
		Env:          []string{"KEEP=1"},
	}
	hc := &container.HostConfig{
		NetworkMode:     "container:abc123",
		PortBindings:    network.PortMap{port: {{HostPort: "8080"}}},
		PublishAllPorts: true,
		DNS:             []netip.Addr{netip.MustParseAddr("1.1.1.1")},
		ExtraHosts:      []string{"a:1.2.3.4"},
		Links:           []string{"db:db"},
		Binds:           []string{"/data:/data"},
	}
	net := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{"x": {}}}

	got := SanitiseForNetworkMode(cfg, hc, net)

	if got != nil {
		t.Errorf("networking config = %+v, want nil for container: mode", got)
	}
	if cfg.Hostname != "" || cfg.Domainname != "" || cfg.ExposedPorts != nil {
		t.Errorf("config kept Hostname=%q Domainname=%q ExposedPorts=%v", cfg.Hostname, cfg.Domainname, cfg.ExposedPorts)
	}
	if hc.PortBindings != nil || hc.PublishAllPorts || hc.DNS != nil || hc.ExtraHosts != nil || hc.Links != nil {
		t.Errorf("host config kept conflicting fields: %+v", hc)
	}
	if hc.NetworkMode != "container:abc123" || len(hc.Binds) != 1 || len(cfg.Env) != 1 {
		t.Errorf("unrelated fields changed: mode=%q binds=%v env=%v", hc.NetworkMode, hc.Binds, cfg.Env)
	}
}

func TestSanitiseForNetworkModeBridgeKeepsEverything(t *testing.T) {
	port := network.MustParsePort("80/tcp")
	cfg := &container.Config{Hostname: "web", ExposedPorts: network.PortSet{port: {}}}
	hc := &container.HostConfig{
		NetworkMode:  "bridge",
		PortBindings: network.PortMap{port: {{HostPort: "8080"}}},
	}
	net := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{"bridge": {}}}

	if got := SanitiseForNetworkMode(cfg, hc, net); got != net {
		t.Errorf("networking config replaced for bridge mode")
	}
	if cfg.Hostname != "web" || len(cfg.ExposedPorts) != 1 || len(hc.PortBindings) != 1 {
		t.Errorf("bridge mode lost fields: cfg=%+v hc=%+v", cfg, hc)
	}
}

func TestSanitiseForNetworkModeUTSHostDropsHostname(t *testing.T) {
	cfg := &container.Config{Hostname: "0123456789ab"}
	hc := &container.HostConfig{NetworkMode: "bridge", UTSMode: "host"}

	SanitiseForNetworkMode(cfg, hc, nil)

	if cfg.Hostname != "" {
		t.Errorf("Hostname = %q, want empty with uts=host (the daemon rejects one)", cfg.Hostname)
	}
}

func TestRecreateConfigDoesNotModifyInspect(t *testing.T) {
	inspect := container.InspectResponse{
		Config: &container.Config{
			Image:    "app:1",
			Hostname: "provider",
			Labels:   map[string]string{"a": "1"},
		},
		HostConfig: &container.HostConfig{NetworkMode: "container:abc", Binds: []string{"/x:/x"}},
	}

	rc := RecreateConfig(inspect, RecreateOptions{Image: "app:2"})
	rc.Config.Labels["b"] = "2"
	rc.HostConfig.Binds[0] = "/y:/y"

	if rc.Config.Image != "app:2" || rc.Config.Hostname != "" {
		t.Errorf("replacement config = %+v", rc.Config)
	}
	if inspect.Config.Image != "app:1" || inspect.Config.Hostname != "provider" || len(inspect.Config.Labels) != 1 {
		t.Errorf("inspect config modified: %+v", inspect.Config)
	}
	if inspect.HostConfig.Binds[0] != "/x:/x" {
		t.Errorf("inspect host config modified: %v", inspect.HostConfig.Binds)
	}
}

func TestRecreateConfigNilHostConfig(t *testing.T) {
	rc := RecreateConfig(container.InspectResponse{Config: &container.Config{Image: "app:1"}}, RecreateOptions{})
	if rc.HostConfig == nil || rc.Config.Image != "app:1" {
		t.Fatalf("RecreateConfig with nil HostConfig = %+v", rc)
	}
}

func volumeMount(name, dest string, rw bool) container.MountPoint {
	return container.MountPoint{Type: mount.TypeVolume, Name: name, Destination: dest, RW: rw}
}

func TestCarryAnonymousVolumesImageVolume(t *testing.T) {
	cfg := &container.Config{Volumes: map[string]struct{}{"/var/lib/postgresql/data/": {}}}
	hc := &container.HostConfig{}

	got := CarryAnonymousVolumes(cfg, hc, []container.MountPoint{volumeMount("anon1", "/var/lib/postgresql/data", true)})

	if !slices.Equal(got, []string{"anon1"}) {
		t.Fatalf("carried = %v, want [anon1]", got)
	}
	want := mount.Mount{Type: mount.TypeVolume, Source: "anon1", Target: "/var/lib/postgresql/data"}
	if len(hc.Mounts) != 1 || hc.Mounts[0] != want {
		t.Errorf("mounts = %+v, want [%+v]", hc.Mounts, want)
	}
}

func TestCarryAnonymousVolumesKeepsReadOnly(t *testing.T) {
	cfg := &container.Config{Volumes: map[string]struct{}{"/cache": {}}}
	hc := &container.HostConfig{}

	CarryAnonymousVolumes(cfg, hc, []container.MountPoint{volumeMount("anon1", "/cache", false)})

	if len(hc.Mounts) != 1 || !hc.Mounts[0].ReadOnly {
		t.Errorf("mounts = %+v, want one read-only mount", hc.Mounts)
	}
}

func TestCarryAnonymousVolumesNamesSourcelessMount(t *testing.T) {
	cfg := &container.Config{}
	hc := &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Target: "/data"}}}

	got := CarryAnonymousVolumes(cfg, hc, []container.MountPoint{volumeMount("anon1", "/data", true)})

	if !slices.Equal(got, []string{"anon1"}) || len(hc.Mounts) != 1 || hc.Mounts[0].Source != "anon1" {
		t.Errorf("carried = %v, mounts = %+v, want the existing mount named anon1", got, hc.Mounts)
	}
}

func TestCarryAnonymousVolumesReplacesBareBind(t *testing.T) {
	cfg := &container.Config{}
	hc := &container.HostConfig{Binds: []string{"/data", "/srv:/srv:ro"}}

	got := CarryAnonymousVolumes(cfg, hc, []container.MountPoint{
		volumeMount("anon1", "/data", true),
		{Type: mount.TypeBind, Source: "/srv", Destination: "/srv"},
	})

	if !slices.Equal(got, []string{"anon1"}) {
		t.Fatalf("carried = %v, want [anon1]", got)
	}
	if !slices.Equal(hc.Binds, []string{"/srv:/srv:ro"}) {
		t.Errorf("binds = %v, want the bare /data entry replaced by a mount", hc.Binds)
	}
	if len(hc.Mounts) != 1 || hc.Mounts[0].Source != "anon1" || hc.Mounts[0].Target != "/data" {
		t.Errorf("mounts = %+v", hc.Mounts)
	}
}

func TestCarryAnonymousVolumesLeavesCoveredDestinations(t *testing.T) {
	cfg := &container.Config{Volumes: map[string]struct{}{"/a": {}, "/b": {}, "/c": {}, "/d": {}}}
	hc := &container.HostConfig{
		Binds:  []string{"named:/a"},
		Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "other", Target: "/b"}},
		Tmpfs:  map[string]string{"/c": ""},
	}

	got := CarryAnonymousVolumes(cfg, hc, []container.MountPoint{
		volumeMount("named", "/a", true),
		volumeMount("other", "/b", true),
		volumeMount("anon-d", "/d", true),
	})

	if !slices.Equal(got, []string{"anon-d"}) {
		t.Errorf("carried = %v, want only the uncovered /d", got)
	}
	if len(hc.Mounts) != 2 || hc.Mounts[1].Source != "anon-d" {
		t.Errorf("mounts = %+v", hc.Mounts)
	}
	if !slices.Equal(hc.Binds, []string{"named:/a"}) {
		t.Errorf("binds changed: %v", hc.Binds)
	}
}

func TestCarryAnonymousVolumesNothingToCarry(t *testing.T) {
	cfg := &container.Config{Volumes: map[string]struct{}{"/data": {}}}
	hc := &container.HostConfig{}

	if got := CarryAnonymousVolumes(cfg, hc, nil); got != nil || hc.Mounts != nil {
		t.Errorf("carried = %v, mounts = %v, want nothing without inspect mounts", got, hc.Mounts)
	}
}

func TestRecreateConfigFreshAnonymousVolumes(t *testing.T) {
	inspect := container.InspectResponse{
		Config:     &container.Config{Volumes: map[string]struct{}{"/data": {}}},
		HostConfig: &container.HostConfig{},
		Mounts:     []container.MountPoint{volumeMount("anon1", "/data", true)},
	}

	rc := RecreateConfig(inspect, RecreateOptions{FreshAnonymousVolumes: true})

	if rc.CarriedVolumes != nil || len(rc.HostConfig.Mounts) != 0 {
		t.Errorf("recreate = %+v, want no volume carried over", rc)
	}
}

func TestRecreateConfigReportsCarriedVolumes(t *testing.T) {
	inspect := container.InspectResponse{
		Config:     &container.Config{Volumes: map[string]struct{}{"/data": {}}},
		HostConfig: &container.HostConfig{},
		Mounts:     []container.MountPoint{volumeMount("anon1", "/data", true)},
	}

	rc := RecreateConfig(inspect, RecreateOptions{})

	if !slices.Equal(rc.CarriedVolumes, []string{"anon1"}) || len(rc.HostConfig.Mounts) != 1 {
		t.Errorf("recreate = %+v", rc)
	}
	if len(inspect.HostConfig.Mounts) != 0 {
		t.Errorf("inspect host config modified: %+v", inspect.HostConfig.Mounts)
	}
}

func TestStripImageDefaultsKeepsUserValues(t *testing.T) {
	img := &container.Config{
		Env:          []string{"PATH=/old/bin", "LANG=C"},
		Labels:       map[string]string{"org.opencontainers.image.version": "1.0", "shared": "image"},
		Entrypoint:   []string{"/entry"},
		Cmd:          []string{"serve"},
		WorkingDir:   "/app",
		User:         "app",
		StopSignal:   "SIGTERM",
		Healthcheck:  &container.HealthConfig{Test: []string{"CMD", "true"}},
		ExposedPorts: network.PortSet{network.MustParsePort("80/tcp"): {}, network.MustParsePort("443/tcp"): {}},
		Volumes:      map[string]struct{}{"/data": {}},
	}
	cfg := &container.Config{
		Env:          []string{"PATH=/custom", "LANG=C", "TOKEN=x"},
		Labels:       map[string]string{"org.opencontainers.image.version": "1.0", "shared": "user", "mine": "1"},
		Entrypoint:   []string{"/my-entry"},
		Cmd:          []string{"serve"},
		WorkingDir:   "/srv",
		User:         "root",
		StopSignal:   "SIGTERM",
		Healthcheck:  &container.HealthConfig{Test: []string{"CMD", "curl", "-f", "localhost"}},
		ExposedPorts: network.PortSet{network.MustParsePort("80/tcp"): {}, network.MustParsePort("9000/tcp"): {}},
		Volumes:      map[string]struct{}{"/data": {}, "/cache": {}},
	}
	hc := &container.HostConfig{PortBindings: network.PortMap{network.MustParsePort("443/tcp"): {{HostPort: "8443"}}}}

	StripImageDefaults(cfg, hc, img)

	if !slices.Equal(cfg.Env, []string{"PATH=/custom", "TOKEN=x"}) {
		t.Errorf("Env = %v", cfg.Env)
	}
	if len(cfg.Labels) != 2 || cfg.Labels["shared"] != "user" || cfg.Labels["mine"] != "1" {
		t.Errorf("Labels = %v", cfg.Labels)
	}
	// An explicit entrypoint stops the daemon filling Cmd from the image,
	// so the Cmd must stay even though it equals the image's.
	if !slices.Equal(cfg.Entrypoint, []string{"/my-entry"}) || !slices.Equal(cfg.Cmd, []string{"serve"}) {
		t.Errorf("Entrypoint = %v Cmd = %v", cfg.Entrypoint, cfg.Cmd)
	}
	if cfg.WorkingDir != "/srv" || cfg.User != "root" || cfg.StopSignal != "" || cfg.Healthcheck == nil {
		t.Errorf("WorkingDir=%q User=%q StopSignal=%q Healthcheck=%v", cfg.WorkingDir, cfg.User, cfg.StopSignal, cfg.Healthcheck)
	}
	wantPorts := network.PortSet{network.MustParsePort("9000/tcp"): {}, network.MustParsePort("443/tcp"): {}}
	if !maps.Equal(cfg.ExposedPorts, wantPorts) {
		t.Errorf("ExposedPorts = %v, want %v (the user's port and the published one)", cfg.ExposedPorts, wantPorts)
	}
	if !maps.Equal(cfg.Volumes, map[string]struct{}{"/cache": {}}) {
		t.Errorf("Volumes = %v", cfg.Volumes)
	}
}

func TestStripImageDefaultsDropsImageEntrypointAndCmd(t *testing.T) {
	img := &container.Config{Entrypoint: []string{"/entry"}, Cmd: []string{"serve"}}
	cfg := &container.Config{Entrypoint: []string{"/entry"}, Cmd: []string{"serve"}, Env: []string{"A=1"}}

	StripImageDefaults(cfg, nil, img)

	if cfg.Entrypoint != nil || cfg.Cmd != nil {
		t.Errorf("Entrypoint = %v Cmd = %v, want both dropped", cfg.Entrypoint, cfg.Cmd)
	}

	cfg = &container.Config{Entrypoint: []string{"/entry"}, Cmd: []string{"worker"}}
	StripImageDefaults(cfg, nil, img)
	if cfg.Entrypoint != nil || !slices.Equal(cfg.Cmd, []string{"worker"}) {
		t.Errorf("Entrypoint = %v Cmd = %v, want the image entrypoint dropped and the user's cmd kept", cfg.Entrypoint, cfg.Cmd)
	}
}

// `--entrypoint /app/bin` with no arguments clears the image's CMD. The
// entrypoint equals the image's, but dropping it would let the daemon add
// the image CMD back, changing the command line.
func TestStripImageDefaultsKeepsEntrypointThatClearedCmd(t *testing.T) {
	img := &container.Config{Entrypoint: []string{"/app/bin"}, Cmd: []string{"serve"}}
	cfg := &container.Config{Entrypoint: []string{"/app/bin"}}

	StripImageDefaults(cfg, nil, img)

	if !slices.Equal(cfg.Entrypoint, []string{"/app/bin"}) || len(cfg.Cmd) != 0 {
		t.Errorf("Entrypoint = %v Cmd = %v, want the entrypoint kept and no cmd", cfg.Entrypoint, cfg.Cmd)
	}
}

// The daemon names a container's host after its short ID when none is
// given, and older daemons list that short ID as a network alias. Carrying
// either over would name the new container after the old one.
func TestRecreateConfigDropsDefaultHostnameAndIDAliases(t *testing.T) {
	inspect := container.InspectResponse{
		ID:         "0123456789abcdef0123456789abcdef",
		Config:     &container.Config{Image: "app:1", Hostname: "0123456789ab"},
		HostConfig: &container.HostConfig{NetworkMode: "app_net"},
		NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{
			"app_net": {Aliases: []string{"web", "0123456789ab"}, Links: []string{"db:db"}, GwPriority: 10},
		}},
	}

	rc := RecreateConfig(inspect, RecreateOptions{})

	if rc.Config.Hostname != "" {
		t.Errorf("Hostname = %q, want the old container's short ID dropped", rc.Config.Hostname)
	}
	ep := rc.NetworkingConfig.EndpointsConfig["app_net"]
	if !slices.Equal(ep.Aliases, []string{"web"}) {
		t.Errorf("Aliases = %v, want [web]", ep.Aliases)
	}
	if !slices.Equal(ep.Links, []string{"db:db"}) || ep.GwPriority != 10 {
		t.Errorf("Links = %v GwPriority = %d, want both carried", ep.Links, ep.GwPriority)
	}

	inspect.Config.Hostname = "web"
	if rc := RecreateConfig(inspect, RecreateOptions{}); rc.Config.Hostname != "web" {
		t.Errorf("a user hostname was dropped: %q", rc.Config.Hostname)
	}
}

func TestRecreateConfigWithoutOldImageKeepsConfig(t *testing.T) {
	inspect := container.InspectResponse{
		Config:     &container.Config{Image: "app:1", Env: []string{"PATH=/old"}, Cmd: []string{"serve"}},
		HostConfig: &container.HostConfig{},
	}

	rc := RecreateConfig(inspect, RecreateOptions{})

	if !slices.Equal(rc.Config.Env, []string{"PATH=/old"}) || !slices.Equal(rc.Config.Cmd, []string{"serve"}) {
		t.Errorf("config changed without OldImage: %+v", rc.Config)
	}
}

func TestNetworkingConfigFromKeepsUserFieldsOnly(t *testing.T) {
	ns := &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{
		"app_net": {
			Aliases:    []string{"web"},
			NetworkID:  "net123",
			IPAddress:  netip.MustParseAddr("172.18.0.5"),
			Gateway:    netip.MustParseAddr("172.18.0.1"),
			EndpointID: "ep1",
		},
	}}

	got := NetworkingConfigFrom(ns)

	ep := got.EndpointsConfig["app_net"]
	if ep == nil || len(ep.Aliases) != 1 || ep.NetworkID != "net123" {
		t.Fatalf("endpoint = %+v", ep)
	}
	if ep.IPAddress.IsValid() || ep.Gateway.IsValid() || ep.EndpointID != "" {
		t.Errorf("runtime fields copied: %+v", ep)
	}
	if NetworkingConfigFrom(nil) != nil || NetworkingConfigFrom(&container.NetworkSettings{}) != nil {
		t.Error("want nil for no networks")
	}
}

// A container an older Sentinel updated was created with the full config of
// the image it ran first: that image's env, labels and command stayed in
// it, and the images after it only added their own new keys. Stripping only
// the current image's values keeps the first image's. Values equal to a
// default of any image the container ran are taken as image-inherited.
func TestRecreateConfigStripsDefaultsOfEarlierImages(t *testing.T) {
	v100 := &container.Config{
		Env:    []string{"PATH=/usr/bin", "APP_VERSION=1.0.0", "IMG_ONLY_V1_0_0=1"},
		Labels: map[string]string{"org.opencontainers.image.version": "1.0.0"},
		Cmd:    []string{"start-v1.0.0"},
	}
	v101 := &container.Config{
		Env:    []string{"PATH=/usr/bin", "APP_VERSION=1.0.1", "IMG_ONLY_V1_0_1=1"},
		Labels: map[string]string{"org.opencontainers.image.version": "1.0.1"},
		Cmd:    []string{"start-v1.0.1"},
	}
	inspect := container.InspectResponse{
		Config: &container.Config{
			Image:  "app:edge",
			Env:    []string{"PATH=/usr/bin", "APP_VERSION=1.0.0", "IMG_ONLY_V1_0_0=1", "IMG_ONLY_V1_0_1=1", "TOKEN=x"},
			Labels: map[string]string{"org.opencontainers.image.version": "1.0.0", "mine": "1"},
			Cmd:    []string{"start-v1.0.0"},
		},
		HostConfig: &container.HostConfig{},
	}

	rc := RecreateConfig(inspect, RecreateOptions{OldImage: v101, InheritedImages: []*container.Config{v100, nil}})

	if !slices.Equal(rc.Config.Env, []string{"TOKEN=x"}) {
		t.Errorf("Env = %v, want only the user's TOKEN", rc.Config.Env)
	}
	if !maps.Equal(rc.Config.Labels, map[string]string{"mine": "1"}) {
		t.Errorf("Labels = %v, want only the user's label", rc.Config.Labels)
	}
	if rc.Config.Cmd != nil {
		t.Errorf("Cmd = %v, want the first image's command dropped", rc.Config.Cmd)
	}
}

// Earlier images count only alongside the current one: without OldImage (a
// rollback, or its config could not be read) nothing is stripped.
func TestRecreateConfigInheritedImagesNeedOldImage(t *testing.T) {
	inspect := container.InspectResponse{
		Config:     &container.Config{Image: "app:1", Env: []string{"APP_VERSION=1.0.0"}},
		HostConfig: &container.HostConfig{},
	}
	v100 := &container.Config{Env: []string{"APP_VERSION=1.0.0"}}

	rc := RecreateConfig(inspect, RecreateOptions{InheritedImages: []*container.Config{v100}})

	if !slices.Equal(rc.Config.Env, []string{"APP_VERSION=1.0.0"}) {
		t.Errorf("Env = %v, want it kept without OldImage", rc.Config.Env)
	}
}

// The entrypoint and cmd are dropped together when both match one image;
// matching the entrypoint of one image and the cmd of another drops only
// the entrypoint, as with a single image.
func TestStripImageDefaultsMatchesCommandPerImage(t *testing.T) {
	a := &container.Config{Entrypoint: []string{"/entry"}, Cmd: []string{"v2"}}
	b := &container.Config{Entrypoint: []string{"/other"}, Cmd: []string{"v1"}}
	c := &container.Config{Entrypoint: []string{"/entry"}, Cmd: []string{"v1"}}

	cfg := &container.Config{Entrypoint: []string{"/entry"}, Cmd: []string{"v1"}}
	StripImageDefaults(cfg, nil, a, b)
	if cfg.Entrypoint != nil || !slices.Equal(cfg.Cmd, []string{"v1"}) {
		t.Errorf("Entrypoint = %v Cmd = %v, want the entrypoint dropped and the cmd kept", cfg.Entrypoint, cfg.Cmd)
	}

	cfg = &container.Config{Entrypoint: []string{"/entry"}, Cmd: []string{"v1"}}
	StripImageDefaults(cfg, nil, a, c)
	if cfg.Entrypoint != nil || cfg.Cmd != nil {
		t.Errorf("Entrypoint = %v Cmd = %v, want both dropped (they match image c)", cfg.Entrypoint, cfg.Cmd)
	}
}
