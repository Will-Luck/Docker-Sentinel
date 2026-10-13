package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/moby/moby/api/types/container"
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
