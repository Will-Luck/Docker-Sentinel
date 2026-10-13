package engine

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

// assertNoNetNamespaceConflicts fails for every create that carries a field
// the daemon rejects for network_mode container:X (runconfig
// validateNetContainerMode: hostname, exposed or published ports, DNS, extra
// hosts, links).
func assertNoNetNamespaceConflicts(t *testing.T, mock *mockDocker) {
	t.Helper()
	if len(mock.createHistory) == 0 {
		t.Fatal("no container was created")
	}
	for i, cfg := range mock.createHistory {
		if cfg.Hostname != "" || cfg.Domainname != "" || len(cfg.ExposedPorts) > 0 {
			t.Errorf("create #%d sent Hostname=%q Domainname=%q ExposedPorts=%v; the daemon rejects these for container: network mode",
				i, cfg.Hostname, cfg.Domainname, cfg.ExposedPorts)
		}
		hc := mock.createHostHistory[i]
		if hc != nil && (len(hc.PortBindings) > 0 || hc.PublishAllPorts || len(hc.DNS) > 0 || len(hc.ExtraHosts) > 0 || len(hc.Links) > 0) {
			t.Errorf("create #%d sent PortBindings=%v PublishAllPorts=%v DNS=%v ExtraHosts=%v Links=%v for container: network mode",
				i, hc.PortBindings, hc.PublishAllPorts, hc.DNS, hc.ExtraHosts, hc.Links)
		}
	}
}

// The daemon copies the provider's hostname into a container:X consumer on
// every start, and stores the image's EXPOSE in its config. Recreating the
// consumer with either makes the create fail, so the update must drop them.
func TestUpdateContainerNetNamespaceConsumerDropsConflictingFields(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	old := mock.inspectResults["aaa"]
	old.Config.Hostname = "gluetun-host"
	old.Config.Domainname = "example.lan"
	old.Config.ExposedPorts = network.PortSet{network.MustParsePort("8080/tcp"): {}}
	old.HostConfig = &container.HostConfig{NetworkMode: "container:0123456789ab"}
	mock.inspectResults["aaa"] = old

	// After its start the replacement carries the provider's hostname too,
	// which the finalise recreate would copy.
	replacement := mock.inspectResults["new-nginx"]
	replacement.Config.Hostname = "gluetun-host"
	replacement.HostConfig = &container.HostConfig{NetworkMode: "container:0123456789ab"}
	mock.inspectResults["new-nginx"] = replacement

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	assertNoNetNamespaceConflicts(t, mock)
	for i, hc := range mock.createHostHistory {
		if hc == nil || hc.NetworkMode != "container:0123456789ab" {
			t.Errorf("create #%d lost the network mode: %+v", i, hc)
		}
	}
}

const pgDataVolume = "3f1c0e9a8b7d6c5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e"

// setupAnonymousVolumeUpdate builds an update of "pg" whose data lives in an
// image-declared VOLUME, which shows up only in Config.Volumes and in
// inspect.Mounts.
func setupAnonymousVolumeUpdate(t *testing.T) (*mockDocker, *Updater) {
	t.Helper()
	mock := newMockDocker()
	mock.mirrorCreate = true
	mock.inspectResults["aaa"] = container.InspectResponse{
		ID:    "aaa",
		Name:  "/pg",
		Image: "sha256:old111",
		Config: &container.Config{
			Image:   "docker.io/library/postgres:16",
			Labels:  map[string]string{},
			Volumes: map[string]struct{}{"/var/lib/postgresql/data": {}},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
		Mounts: []container.MountPoint{{
			Type: mount.TypeVolume, Name: pgDataVolume, Destination: "/var/lib/postgresql/data", Driver: "local", RW: true,
		}},
	}
	mock.imageIDs["docker.io/library/postgres:16"] = "sha256:new222"
	u, _ := newTestUpdater(t, mock)
	return mock, u
}

// hostConfigUsesVolume reports whether a create passed the named volume.
func hostConfigUsesVolume(hc *container.HostConfig, name string) bool {
	if hc == nil {
		return false
	}
	for _, b := range hc.Binds {
		if strings.HasPrefix(b, name+":") {
			return true
		}
	}
	for _, m := range hc.Mounts {
		if m.Type == mount.TypeVolume && m.Source == name {
			return true
		}
	}
	return false
}

// Without the old volume the daemon creates a fresh empty one for each
// VOLUME path, so postgres re-runs initdb and the update looks successful.
func TestUpdateContainerCarriesAnonymousVolumes(t *testing.T) {
	mock, u := setupAnonymousVolumeUpdate(t)

	if err := u.UpdateContainer(context.Background(), "aaa", "pg", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	if len(mock.createHostHistory) != 2 {
		t.Fatalf("creates = %d, want 2 (update, then finalise)", len(mock.createHostHistory))
	}
	for i, hc := range mock.createHostHistory {
		if !hostConfigUsesVolume(hc, pgDataVolume) {
			t.Errorf("create #%d did not pass the old anonymous volume (Binds=%v Mounts=%v); the daemon would create an empty one",
				i, hc.Binds, hc.Mounts)
		}
	}
}

// remove_volumes (or the sentinel.remove-volumes label) starts the new
// container on fresh anonymous volumes, like `docker compose up -V`: the old
// ones are not carried over and are removed with the old container.
func TestUpdateContainerRemoveVolumesStartsOnFreshVolumes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*mockDocker, *Updater)
	}{
		{"setting", func(_ *mockDocker, u *Updater) { u.cfg.SetRemoveVolumes(true) }},
		{"label", func(m *mockDocker, _ *Updater) {
			old := m.inspectResults["aaa"]
			old.Config.Labels["sentinel.remove-volumes"] = "true"
			m.inspectResults["aaa"] = old
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock, u := setupAnonymousVolumeUpdate(t)
			tc.setup(mock, u)

			if err := u.UpdateContainer(context.Background(), "aaa", "pg", ""); err != nil {
				t.Fatalf("UpdateContainer: %v", err)
			}
			if hostConfigUsesVolume(mock.createHostHistory[0], pgDataVolume) {
				t.Errorf("the old anonymous volume was carried over with remove_volumes on: %+v", mock.createHostHistory[0].Mounts)
			}
			if !slices.Equal(mock.removeWithVolumesCalls, []string{"aaa"}) {
				t.Errorf("removeWithVolumesCalls = %v, want the old container removed with its volumes", mock.removeWithVolumesCalls)
			}
		})
	}
}

// With remove_volumes off (the default) the old container is removed
// without its volumes, which the new container now uses.
func TestUpdateContainerKeepsCarriedVolumesByDefault(t *testing.T) {
	mock, u := setupAnonymousVolumeUpdate(t)

	if err := u.UpdateContainer(context.Background(), "aaa", "pg", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	if len(mock.removeWithVolumesCalls) != 0 {
		t.Errorf("removed %v with its volumes, deleting the data volume the new container uses", mock.removeWithVolumesCalls)
	}
}

func TestRollbackCarriesAnonymousVolumes(t *testing.T) {
	mock := newMockDocker()
	snap := container.InspectResponse{
		ID:   "old-id",
		Name: "/pg",
		Config: &container.Config{
			Image:   "postgres:16",
			Volumes: map[string]struct{}{"/var/lib/postgresql/data": {}},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
		Mounts: []container.MountPoint{{
			Type: mount.TypeVolume, Name: pgDataVolume, Destination: "/var/lib/postgresql/data", RW: true,
		}},
	}
	data, _ := json.Marshal(snap)

	if err := runRollback(mock, "pg", data); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !hostConfigUsesVolume(mock.createHostHistory[0], pgDataVolume) {
		t.Errorf("rollback create did not pass the old anonymous volume: %+v", mock.createHostHistory[0])
	}
}

// oldPostgresImage is the config of the image "aaa" was created from in
// setupImageDefaultsUpdate.
func oldPostgresImage() *container.Config {
	return &container.Config{
		Env:         []string{"PATH=/usr/lib/postgresql/16/bin:/usr/bin", "PG_MAJOR=16"},
		Labels:      map[string]string{"org.opencontainers.image.version": "16.1"},
		Entrypoint:  []string{"docker-entrypoint.sh"},
		Cmd:         []string{"postgres"},
		WorkingDir:  "/",
		User:        "postgres",
		StopSignal:  "SIGINT",
		Healthcheck: &container.HealthConfig{Test: []string{"CMD", "pg_isready"}},
		ExposedPorts: network.PortSet{
			network.MustParsePort("5432/tcp"): {},
		},
		Volumes: map[string]struct{}{"/var/lib/postgresql/data": {}},
	}
}

// The old image declares the data VOLUME, so the strip drops it from
// Config.Volumes; the old volume must still be carried over (and the new
// image's VOLUME, filled back in by the daemon, is then covered by it).
func TestUpdateContainerCarriesVolumeTheStripDrops(t *testing.T) {
	mock, u := setupAnonymousVolumeUpdate(t)
	mock.imageConfigs["sha256:old111"] = oldPostgresImage()

	if err := u.UpdateContainer(context.Background(), "aaa", "pg", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	cfg, hc := mock.createHistory[0], mock.createHostHistory[0]
	if _, kept := cfg.Volumes["/var/lib/postgresql/data"]; kept {
		t.Errorf("Volumes = %v, want the image-declared path left to the new image", cfg.Volumes)
	}
	if !hostConfigUsesVolume(hc, pgDataVolume) {
		t.Errorf("old data volume not carried over: %+v", hc.Mounts)
	}
}

// A rollback takes over the running container's anonymous volume at the
// same path, so it starts on the current data rather than on the volume
// the snapshot recorded.
func TestRollbackUsesLiveContainersVolumes(t *testing.T) {
	f := newFakeDocker()
	live := f.add("live", "pg", "postgres:16", true, &container.Config{Volumes: map[string]struct{}{"/var/lib/postgresql/data": {}}}, nil)
	live.mounts = []container.MountPoint{{Type: mount.TypeVolume, Name: "current-data", Destination: "/var/lib/postgresql/data", RW: true}}
	snap, _ := json.Marshal(container.InspectResponse{
		ID: "old", Name: "/pg",
		Config:     &container.Config{Image: "postgres:16", Volumes: map[string]struct{}{"/var/lib/postgresql/data": {}}},
		HostConfig: &container.HostConfig{},
		Mounts:     []container.MountPoint{{Type: mount.TypeVolume, Name: "snapshot-data", Destination: "/var/lib/postgresql/data", RW: true}},
	})

	if err := runRollback(f, "pg", snap); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	c := f.byName("pg")
	if c == nil || c.id == "live" {
		t.Fatalf("containers = %v, want a rollback container", f.names())
	}
	if !hostConfigUsesVolume(c.host, "current-data") || hostConfigUsesVolume(c.host, "snapshot-data") {
		t.Errorf("rollback mounts = %+v, want the live container's volume", c.host.Mounts)
	}
}

// setupImageDefaultsUpdate builds an update whose old container config is
// the daemon's merge of the old image's defaults and the user's own values.
func setupImageDefaultsUpdate(t *testing.T) (*mockDocker, *Updater) {
	t.Helper()
	mock, u := setupUpdateWithScanVerify(t)
	img := oldPostgresImage()
	mock.imageConfigs["sha256:old111"] = img

	old := mock.inspectResults["aaa"]
	old.Config = &container.Config{
		Image:        "docker.io/library/nginx:latest",
		Env:          append([]string{"POSTGRES_PASSWORD=secret"}, img.Env...),
		Labels:       map[string]string{"org.opencontainers.image.version": "16.1", "com.example.owner": "ops"},
		Entrypoint:   img.Entrypoint,
		Cmd:          img.Cmd,
		WorkingDir:   img.WorkingDir,
		User:         img.User,
		StopSignal:   img.StopSignal,
		Healthcheck:  &container.HealthConfig{Test: []string{"CMD", "pg_isready"}},
		ExposedPorts: network.PortSet{network.MustParsePort("5432/tcp"): {}},
	}
	mock.inspectResults["aaa"] = old
	return mock, u
}

// The daemon fills a field from the image only where the create request
// leaves it empty, so copying the old container's merged config keeps the
// old image's env, labels, entrypoint, healthcheck and so on, and the new
// image's values never apply.
func TestUpdateContainerDropsOldImageDefaults(t *testing.T) {
	mock, u := setupImageDefaultsUpdate(t)

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	cfg := mock.createHistory[0]
	if !slices.Equal(cfg.Env, []string{"POSTGRES_PASSWORD=secret"}) {
		t.Errorf("Env = %v, want only the user's POSTGRES_PASSWORD (old image PATH and PG_MAJOR dropped)", cfg.Env)
	}
	if _, ok := cfg.Labels["org.opencontainers.image.version"]; ok {
		t.Errorf("old image version label kept: %v", cfg.Labels)
	}
	if cfg.Labels["com.example.owner"] != "ops" || cfg.Labels["sentinel.maintenance"] != "true" {
		t.Errorf("user or Sentinel labels lost: %v", cfg.Labels)
	}
	if cfg.Entrypoint != nil || cfg.Cmd != nil || cfg.WorkingDir != "" || cfg.User != "" || cfg.StopSignal != "" || cfg.Healthcheck != nil {
		t.Errorf("old image defaults kept: Entrypoint=%v Cmd=%v WorkingDir=%q User=%q StopSignal=%q Healthcheck=%v",
			cfg.Entrypoint, cfg.Cmd, cfg.WorkingDir, cfg.User, cfg.StopSignal, cfg.Healthcheck)
	}
	if len(cfg.ExposedPorts) != 0 {
		t.Errorf("ExposedPorts = %v, want the old image's EXPOSE dropped", cfg.ExposedPorts)
	}
}

// A rollback recreates the snapshot as it was, image defaults included.
func TestRollbackKeepsSnapshotConfig(t *testing.T) {
	mock := newMockDocker()
	img := oldPostgresImage()
	mock.imageConfigs["postgres:16"] = img
	snap := container.InspectResponse{
		ID:              "old-id",
		Name:            "/pg",
		Config:          &container.Config{Image: "postgres:16", Env: img.Env, Cmd: img.Cmd},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}
	data, _ := json.Marshal(snap)

	if err := runRollback(mock, "pg", data); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	cfg := mock.createHistory[0]
	if !slices.Equal(cfg.Env, img.Env) || !slices.Equal(cfg.Cmd, img.Cmd) {
		t.Errorf("rollback changed the snapshot config: Env=%v Cmd=%v", cfg.Env, cfg.Cmd)
	}
}

func TestRollbackNetNamespaceConsumerDropsConflictingFields(t *testing.T) {
	mock := newMockDocker()
	snap := container.InspectResponse{
		ID:   "old-id",
		Name: "/qbittorrent",
		Config: &container.Config{
			Image:        "qbittorrent:4.6",
			Hostname:     "gluetun-host",
			ExposedPorts: network.PortSet{network.MustParsePort("8080/tcp"): {}},
		},
		HostConfig:      &container.HostConfig{NetworkMode: "container:0123456789ab"},
		NetworkSettings: &container.NetworkSettings{},
	}
	data, _ := json.Marshal(snap)

	if err := runRollback(mock, "qbittorrent", data); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	assertNoNetNamespaceConflicts(t, mock)
}
