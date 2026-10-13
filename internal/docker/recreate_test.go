package docker

import (
	"net/netip"
	"testing"

	"github.com/moby/moby/api/types/container"
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
