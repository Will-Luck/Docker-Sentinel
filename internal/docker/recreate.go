package docker

import (
	"maps"
	"path"
	"reflect"
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
	// OldImage is the config of the image the inspected container was
	// created from. When set, values the container got from that image
	// rather than from the user are dropped (see StripImageDefaults), so the
	// daemon fills them from the new image. Leave it nil to recreate the
	// container as it was, as a rollback does.
	OldImage *container.Config
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
	if opts.OldImage != nil {
		StripImageDefaults(cfg, hostCfg, opts.OldImage)
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

// StripImageDefaults removes from cfg the values that came from the image
// the container was created from (img) rather than from the user, the way
// Watchtower does.
//
// The daemon merges an image's defaults into the container config at create
// time, filling a field only where the request leaves it empty. A container's
// inspect config is that merged result, so copying it to a new container
// would keep the old image's values and the new image's would never apply:
// a stale PATH, the old version label, the old entrypoint and healthcheck.
// After stripping, the daemon fills those fields from the new image. A value
// the user set that happens to equal the old image's default is dropped too;
// the two cannot be told apart.
//
// Dropped: env entries equal to an image entry, labels with the image's key
// and value, the entrypoint (and then the cmd) when equal to the image's,
// an equal healthcheck, working dir, user and stop signal, and the image's
// exposed ports and volumes. Ports that are published stay exposed.
func StripImageDefaults(cfg *container.Config, hostCfg *container.HostConfig, img *container.Config) {
	if cfg == nil || img == nil {
		return
	}
	cfg.Env = slices.DeleteFunc(cfg.Env, func(e string) bool { return slices.Contains(img.Env, e) })
	if len(cfg.Env) == 0 {
		cfg.Env = nil
	}
	for k, v := range img.Labels {
		if cv, ok := cfg.Labels[k]; ok && cv == v {
			delete(cfg.Labels, k)
		}
	}
	// The daemon fills Cmd from the image only when Entrypoint is empty, so
	// Cmd can come from the image only if Entrypoint did.
	if slices.Equal(cfg.Entrypoint, img.Entrypoint) {
		cfg.Entrypoint = nil
		if slices.Equal(cfg.Cmd, img.Cmd) {
			cfg.Cmd = nil
		}
	}
	if cfg.Healthcheck != nil && img.Healthcheck != nil && reflect.DeepEqual(*cfg.Healthcheck, *img.Healthcheck) {
		cfg.Healthcheck = nil
	}
	if cfg.WorkingDir == img.WorkingDir {
		cfg.WorkingDir = ""
	}
	if cfg.User == img.User {
		cfg.User = ""
	}
	if cfg.StopSignal == img.StopSignal {
		cfg.StopSignal = ""
	}
	for p := range img.ExposedPorts {
		delete(cfg.ExposedPorts, p)
	}
	if hostCfg != nil {
		for p := range hostCfg.PortBindings {
			if cfg.ExposedPorts == nil {
				cfg.ExposedPorts = make(network.PortSet)
			}
			cfg.ExposedPorts[p] = struct{}{}
		}
	}
	if len(cfg.ExposedPorts) == 0 {
		cfg.ExposedPorts = nil
	}
	for v := range img.Volumes {
		delete(cfg.Volumes, v)
	}
	if len(cfg.Volumes) == 0 {
		cfg.Volumes = nil
	}
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
