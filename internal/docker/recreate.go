package docker

import (
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

// Recreate is the create request for a container that replaces another one.
type Recreate struct {
	Config           *container.Config
	HostConfig       *container.HostConfig
	NetworkingConfig *network.NetworkingConfig
	// CarriedVolumes names the old container's anonymous volumes that the
	// replacement mounts. Removing the old container must keep them.
	CarriedVolumes []string
}

// RecreateOptions controls how RecreateConfig builds the replacement.
type RecreateOptions struct {
	// Image is the image reference for the replacement. Empty keeps the
	// image reference of the inspected container.
	Image string
}

// RecreateConfig builds the create request for a container that replaces
// the one described by inspect, from what the daemon reports about it. The
// replacement mounts the old container's anonymous volumes (see
// CarryAnonymousVolumes), and fields the daemon fills in itself, and would
// reject on create, are removed (see SanitiseForNetworkMode). The inspect
// value is not modified.
func RecreateConfig(inspect container.InspectResponse, opts RecreateOptions) Recreate {
	cfg := cloneConfig(inspect.Config)
	hostCfg := cloneHostConfig(inspect.HostConfig)
	carried := CarryAnonymousVolumes(cfg, hostCfg, inspect.Mounts)
	if opts.Image != "" {
		cfg.Image = opts.Image
	}
	netCfg := NetworkingConfigFrom(inspect.NetworkSettings)
	netCfg = SanitiseForNetworkMode(cfg, hostCfg, netCfg)
	return Recreate{Config: cfg, HostConfig: hostCfg, NetworkingConfig: netCfg, CarriedVolumes: carried}
}

// CarryAnonymousVolumes makes the replacement of a container mount the
// anonymous volumes the old one uses, as docker compose does, and returns
// their names.
//
// An anonymous volume is one the daemon names itself: an image VOLUME or a
// bare `-v /path` (both listed in Config.Volumes), a `--mount type=volume`
// without a source, or a Binds entry without a source. On create the daemon
// makes a fresh, empty volume for each of them, so without this the new
// container starts on an empty data directory and the old data is left in
// an orphaned volume. The existing volume is passed by name instead, from
// mounts (the inspect Mounts of the old container). Destinations that a
// bind, a named volume or a tmpfs already covers are left alone.
func CarryAnonymousVolumes(cfg *container.Config, hostCfg *container.HostConfig, mounts []container.MountPoint) []string {
	if cfg == nil || hostCfg == nil {
		return nil
	}
	existing := make(map[string]container.MountPoint)
	for _, m := range mounts {
		if m.Type == mount.TypeVolume && m.Name != "" {
			existing[path.Clean(m.Destination)] = m
		}
	}
	if len(existing) == 0 {
		return nil
	}

	var carried []string
	covered := make(map[string]bool)
	anonymous := make(map[string]bool)

	for _, b := range hostCfg.Binds {
		parts := strings.Split(b, ":")
		if len(parts) == 1 {
			anonymous[path.Clean(parts[0])] = true
			continue
		}
		covered[path.Clean(parts[1])] = true
	}
	for i := range hostCfg.Mounts {
		m := &hostCfg.Mounts[i]
		dest := path.Clean(m.Target)
		covered[dest] = true
		if m.Type != mount.TypeVolume || m.Source != "" {
			continue
		}
		if old, ok := existing[dest]; ok {
			m.Source = old.Name
			carried = append(carried, old.Name)
		}
	}
	for dest := range hostCfg.Tmpfs {
		covered[path.Clean(dest)] = true
	}
	for dest := range cfg.Volumes {
		anonymous[path.Clean(dest)] = true
	}

	dests := slices.Sorted(maps.Keys(anonymous))
	for _, dest := range dests {
		old, ok := existing[dest]
		if !ok || covered[dest] {
			continue
		}
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{
			Type:     mount.TypeVolume,
			Source:   old.Name,
			Target:   old.Destination,
			ReadOnly: !old.RW,
		})
		hostCfg.Binds = slices.DeleteFunc(hostCfg.Binds, func(b string) bool {
			return !strings.Contains(b, ":") && path.Clean(b) == dest
		})
		carried = append(carried, old.Name)
	}
	return carried
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
