package agent

import (
	"context"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
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

// F2: the helper's final outcome arrives after the agent started. When it does,
// the failure is journalled and sent to the hub at once, not at the next
// reconnect.
func TestAgentFlushesTheJournalToTheOpenSession(t *testing.T) {
	dir := t.TempDir()
	a := newTestAgent(dir, newMockDocker())
	j, err := newJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.journal = j
	stream := &recStream{}
	a.setStream(stream)

	a.reportSelfUpdateOutcome(&selfupdate.Outcome{Result: selfupdate.ResultRolledBack, Error: "boom", ContainerName: "agent", FinishedAt: time.Now()})
	a.flushJournal()

	sent := stream.messages()
	if len(sent) != 1 || sent[0].GetOfflineJournal() == nil || len(sent[0].GetOfflineJournal().Entries) != 1 {
		t.Fatalf("sent = %+v, want one offline journal with one entry", sent)
	}
	if j.Len() != 0 {
		t.Errorf("journal still holds %d entries after the flush", j.Len())
	}

	// With no session the entry waits for the next one.
	a.setStream(nil)
	a.reportSelfUpdateOutcome(&selfupdate.Outcome{Result: selfupdate.ResultFailed, Error: "x", ContainerName: "agent", FinishedAt: time.Now().Add(time.Second)})
	a.flushJournal()
	if j.Len() != 1 {
		t.Errorf("journal entries = %d, want the entry kept for the next session", j.Len())
	}
}

func twoSentinelHost(t *testing.T) (*mockDocker, *Agent) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		sentinelSummary("other99other99", "lh2-hub", "sentinel:v1"),
		sentinelSummary("bbbbbbbbbbbb2222", "lh-sentinel-agent", "sentinel:v1"),
	}
	for _, id := range []string{"other99other99", "bbbbbbbbbbbb2222"} {
		in := sentinelInspect("sentinel:v1")
		in.ID = id
		mock.inspectResults[id] = in
	}
	return mock, newTestAgent(t.TempDir(), mock)
}

func updateResultOf(t *testing.T, a *Agent, name string) *proto.UpdateResult {
	t.Helper()
	st := &recStream{}
	req := &proto.UpdateContainerRequest{ContainerName: name, TargetImage: "sentinel:v2"}
	if err := a.handleUpdateContainer(context.Background(), st, req, "req-1"); err != nil {
		t.Fatal(err)
	}
	for _, m := range st.messages() {
		if ur := m.GetUpdateResult(); ur != nil {
			return ur
		}
	}
	t.Fatal("no UpdateResult")
	return nil
}

// The hub names the first Sentinel container it saw on the host. When that is
// another Sentinel, the agent still updates its own container, found by its ID,
// and leaves the other alone.
func TestAgentSelfUpdateTargetsItsOwnContainerWhateverNameTheHubSent(t *testing.T) {
	mock, a := twoSentinelHost(t)
	a.selfID = func() string { return "bbbbbbbbbbbb" }
	a.cfg.DockerSock = "tcp://example:2375" // in-process path: observable without a helper

	res := updateResultOf(t, a, "lh2-hub")

	if res.GetOutcome() != "success" {
		t.Fatalf("result = %+v", res)
	}
	for _, r := range mock.renameCalls {
		if r.id == "other99other99" {
			t.Errorf("renamed the other Sentinel: %+v", mock.renameCalls)
		}
	}
	if mock.createConfigs["lh-sentinel-agent"] == nil || mock.createConfigs["lh2-hub"] != nil {
		t.Errorf("replacement created for the wrong container: %v", mock.createConfigs)
	}
	for _, id := range mock.removeCalls {
		if id == "other99other99" {
			t.Errorf("removed the other Sentinel")
		}
	}
}

func TestAgentSelfUpdateRefusesWhenItCannotTellWhichContainerItIs(t *testing.T) {
	for _, id := range []string{"", "ffffffffffff"} {
		mock, a := twoSentinelHost(t)
		a.selfID = func() string { return id }
		res := updateResultOf(t, a, "lh2-hub")
		if res.GetOutcome() != "failed" || !strings.Contains(res.GetError(), "could not determine") {
			t.Errorf("id %q: result = %+v", id, res)
		}
		if len(mock.renameCalls) != 0 || len(mock.createConfigs) != 0 || len(mock.pullCalls) != 0 {
			t.Errorf("id %q: something changed: renames=%v creates=%v pulls=%v", id, mock.renameCalls, mock.createConfigs, mock.pullCalls)
		}
	}
}
