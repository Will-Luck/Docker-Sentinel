package docker

import (
	"maps"
	"slices"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// Recreate is the create request for a container that replaces another one.
type Recreate struct {
	Config           *container.Config
	HostConfig       *container.HostConfig
	NetworkingConfig *network.NetworkingConfig
}

// RecreateOptions controls how RecreateConfig builds the replacement.
type RecreateOptions struct {
	// Image is the image reference for the replacement. Empty keeps the
	// image reference of the inspected container.
	Image string
}

// RecreateConfig builds the create request for a container that replaces
// the one described by inspect, from what the daemon reports about it.
// Fields the daemon fills in itself, and would reject on create, are
// removed (see SanitiseForNetworkMode). The inspect value is not modified.
func RecreateConfig(inspect container.InspectResponse, opts RecreateOptions) Recreate {
	cfg := cloneConfig(inspect.Config)
	hostCfg := cloneHostConfig(inspect.HostConfig)
	if opts.Image != "" {
		cfg.Image = opts.Image
	}
	netCfg := NetworkingConfigFrom(inspect.NetworkSettings)
	netCfg = SanitiseForNetworkMode(cfg, hostCfg, netCfg)
	return Recreate{Config: cfg, HostConfig: hostCfg, NetworkingConfig: netCfg}
}

// SanitiseForNetworkMode removes the fields the daemon filled in on the
// running container that it refuses in a create request, and returns the
// networking config to create with.
//
// A container in another container's network namespace (network_mode
// container:X, compose service:X) gets the provider's hostname and domain
// name copied in on every start, and the image's EXPOSE stored in its
// config. The daemon rejects a create in that mode that carries a hostname,
// exposed or published ports, DNS servers, extra hosts or links, and it has
// no endpoints of its own, so the networking config is dropped too. With
// uts=host the daemon fills the hostname in at create and rejects one sent
// by the client.
func SanitiseForNetworkMode(cfg *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig) *network.NetworkingConfig {
	if cfg == nil || hostCfg == nil {
		return netCfg
	}
	if hostCfg.UTSMode.IsHost() {
		cfg.Hostname = ""
	}
	if !hostCfg.NetworkMode.IsContainer() {
		return netCfg
	}
	cfg.Hostname = ""
	cfg.Domainname = ""
	cfg.ExposedPorts = nil
	hostCfg.PortBindings = nil
	hostCfg.PublishAllPorts = false
	hostCfg.DNS = nil
	hostCfg.ExtraHosts = nil
	hostCfg.Links = nil
	return nil
}

// NetworkingConfigFrom rebuilds the endpoint settings of a container's
// networks for a create request. Only what the user configured is kept
// (IPAM config, aliases, driver options, network ID and MAC address), not
// runtime fields such as the gateway or the assigned IP address.
func NetworkingConfigFrom(ns *container.NetworkSettings) *network.NetworkingConfig {
	if ns == nil || len(ns.Networks) == 0 {
		return nil
	}

	endpoints := make(map[string]*network.EndpointSettings)
	for netName, ep := range ns.Networks {
		if ep == nil {
			continue
		}
		endpoints[netName] = &network.EndpointSettings{
			IPAMConfig: ep.IPAMConfig,
			Aliases:    ep.Aliases,
			DriverOpts: ep.DriverOpts,
			NetworkID:  ep.NetworkID,
			MacAddress: ep.MacAddress,
		}
	}
	return &network.NetworkingConfig{EndpointsConfig: endpoints}
}

// cloneConfig copies a container config deeply enough that the recreate
// helpers can change the copy without touching the original.
func cloneConfig(cfg *container.Config) *container.Config {
	if cfg == nil {
		return &container.Config{}
	}
	c := *cfg
	c.Labels = maps.Clone(cfg.Labels)
	c.Env = slices.Clone(cfg.Env)
	c.Cmd = slices.Clone(cfg.Cmd)
	c.Entrypoint = slices.Clone(cfg.Entrypoint)
	c.ExposedPorts = maps.Clone(cfg.ExposedPorts)
	c.Volumes = maps.Clone(cfg.Volumes)
	return &c
}

// cloneHostConfig copies a host config deeply enough that the recreate
// helpers can change the copy without touching the original.
func cloneHostConfig(hc *container.HostConfig) *container.HostConfig {
	if hc == nil {
		return &container.HostConfig{}
	}
	c := *hc
	c.Binds = slices.Clone(hc.Binds)
	c.Mounts = slices.Clone(hc.Mounts)
	c.PortBindings = maps.Clone(hc.PortBindings)
	return &c
}
