package docker

import (
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
