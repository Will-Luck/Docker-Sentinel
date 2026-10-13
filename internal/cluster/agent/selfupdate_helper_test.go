package agent

import (
	"context"
	"github.com/Will-Luck/Docker-Sentinel/internal/selfupdate"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

// An agent with the Docker socket mounted hands its own update to a helper
// container: it sets the replacement up (published ports and all) but neither
// starts it nor stops itself, because it cannot free its own ports.
func TestSelfUpdateContainerHandsOverToHelperWhenSocketIsMounted(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelSummary("s-1", "sentinel-agent", "sentinel:v1")}
	inspect := sentinelInspect("sentinel:v1")
	inspect.ID = "s-1"
	inspect.HostConfig.NetworkMode = "bridge"
	inspect.HostConfig.PortBindings = map[network.Port][]network.PortBinding{network.MustParsePort("8889/tcp"): {{HostPort: "8889"}}}
	inspect.Mounts = []container.MountPoint{
		{Type: mount.TypeBind, Source: "/run/docker.sock", Destination: "/var/run/docker.sock"},
		{Type: mount.TypeVolume, Name: "agent-data", Destination: "/data"},
	}
	mock.inspectResults["s-1"] = inspect

	a := newTestAgent(t.TempDir(), mock)
	a.cfg.DockerSock = "/var/run/docker.sock"
	a.cfg.DataDir = "/data/cluster"
	r, err := a.selfUpdateContainer(context.Background(), "sentinel-agent", "sentinel:v2")
	if err != nil {
		t.Fatal(err)
	}
	if r.handover == nil || r.oldContainerID != "" {
		t.Fatalf("handover=%v oldContainerID=%q, want a handover and no in-process stop", r.handover, r.oldContainerID)
	}
	if len(mock.stopCalls) != 0 {
		t.Errorf("the agent stopped something itself: %v", mock.stopCalls)
	}
	repl := mock.createConfigs["sentinel-agent"]
	if repl == nil || repl.Image != "sentinel:v2" {
		t.Fatalf("replacement config = %+v", repl)
	}
	helper := mock.createConfigs["sentinel-agent-self-update-helper"]
	if helper == nil || helper.Image != "sentinel:v1" || !slices.Equal(helper.Cmd, []string{"self-update-helper"}) {
		t.Fatalf("helper config = %+v", helper)
	}
	// Only the helper was started; it starts the replacement.
	if len(mock.startCalls) != 1 {
		t.Errorf("start calls = %v, want only the helper", mock.startCalls)
	}
}

// The agent reports success to the hub as soon as the helper starts. If the
// swap is then rolled back, the new instance tells the hub, so its history and
// notifications do not keep saying the agent was updated.
func TestAgentJournalsARolledBackSelfUpdateForTheHub(t *testing.T) {
	dir := t.TempDir()
	a := newTestAgent(dir, newMockDocker())
	j, err := newJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.journal = j

	a.reportSelfUpdateOutcome(&selfupdate.Outcome{
		Result: selfupdate.ResultRolledBack, Error: "the new container stopped or is restarting",
		ContainerName: "sentinel-agent", OldImage: "sentinel:v1", NewImage: "sentinel:v2",
		FinishedAt: time.Now(),
	})

	entries := j.Entries()
	if len(entries) != 1 {
		t.Fatalf("journal entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Outcome != "failed" || e.Container != "sentinel-agent" || e.NewImage != "sentinel:v2" ||
		!strings.Contains(e.Error, "rolled back") || !strings.Contains(e.Error, "restarting") {
		t.Errorf("entry = %+v", e)
	}

	// A successful swap, or none, adds nothing.
	a.reportSelfUpdateOutcome(&selfupdate.Outcome{Result: selfupdate.ResultSuccess})
	a.reportSelfUpdateOutcome(nil)
	if n := j.Len(); n != 1 {
		t.Errorf("journal entries = %d after a success and nil, want 1", n)
	}
}
