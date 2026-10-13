package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
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

// remove_volumes must never delete a volume the replacement now uses.
func TestUpdateContainerRemoveVolumesKeepsCarriedVolumes(t *testing.T) {
	mock, u := setupAnonymousVolumeUpdate(t)
	u.cfg.SetRemoveVolumes(true)

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

	if err := rollback(context.Background(), mock, "pg", data, logging.New(false)); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !hostConfigUsesVolume(mock.createHostHistory[0], pgDataVolume) {
		t.Errorf("rollback create did not pass the old anonymous volume: %+v", mock.createHostHistory[0])
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

	if err := rollback(context.Background(), mock, "qbittorrent", data, logging.New(false)); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	assertNoNetNamespaceConflicts(t, mock)
}
