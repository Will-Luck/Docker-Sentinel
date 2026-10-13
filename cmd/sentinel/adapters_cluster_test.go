package main

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/agent"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/web"
)

// idleDocker is an agent DockerAPI for a host with no containers.
type idleDocker struct{}

func (idleDocker) ListContainers(context.Context) ([]container.Summary, error)    { return nil, nil }
func (idleDocker) ListAllContainers(context.Context) ([]container.Summary, error) { return nil, nil }
func (idleDocker) InspectContainer(context.Context, string) (container.InspectResponse, error) {
	return container.InspectResponse{}, nil
}
func (idleDocker) StopContainer(context.Context, string, int) error { return nil }
func (idleDocker) RemoveContainer(context.Context, string) error    { return nil }
func (idleDocker) RemoveContainerWithVolumes(context.Context, string) error {
	return nil
}
func (idleDocker) CreateContainer(context.Context, string, *container.Config, *container.HostConfig, *network.NetworkingConfig) (string, error) {
	return "", nil
}
func (idleDocker) StartContainer(context.Context, string) error               { return nil }
func (idleDocker) RestartContainer(context.Context, string) error             { return nil }
func (idleDocker) RenameContainer(context.Context, string, string) error      { return nil }
func (idleDocker) NetworkConnect(context.Context, string, string) error       { return nil }
func (idleDocker) PullImage(context.Context, string) error                    { return nil }
func (idleDocker) ImageDigest(context.Context, string) (string, error)        { return "", nil }
func (idleDocker) ContainerLogs(context.Context, string, int) (string, error) { return "", nil }
func (idleDocker) EngineID(context.Context) (string, error)                   { return "engine-1", nil }
func (idleDocker) ExecContainer(context.Context, string, []string, int) (int, string, error) {
	return 0, "", nil
}

// Turning cluster mode off in Settings while an agent is connected must
// return, and turning it back on must work and let the agent reconnect.
func TestClusterManager_StopWithConnectedAgentThenRestart(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/sentinel.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	lis.Close()
	if err := db.SaveSetting(store.SettingClusterPort, strconv.Itoa(port)); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	log := slog.New(slog.DiscardHandler)
	cm := &clusterManager{
		db:      db,
		bus:     events.New(),
		log:     log,
		updater: &engine.Updater{},
		ctrl:    web.NewClusterController(),
		dataDir: t.TempDir(),
	}
	if err := cm.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(cm.Stop)

	cm.mu.Lock()
	token, _, err := cm.srv.GenerateEnrollToken(time.Minute)
	cm.mu.Unlock()
	if err != nil {
		t.Fatalf("GenerateEnrollToken: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	agentDone := make(chan struct{})
	ag := agent.New(agent.Config{
		ServerAddr:  net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		EnrollToken: token,
		HostName:    "agent-host",
		DataDir:     t.TempDir(),
	}, idleDocker{}, log)
	go func() {
		defer close(agentDone)
		_ = ag.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-agentDone
	})

	connectedHosts := func() int {
		cm.mu.Lock()
		defer cm.mu.Unlock()
		if cm.srv == nil {
			return 0
		}
		return len(cm.srv.ConnectedHosts())
	}
	waitUntil(t, 10*time.Second, func() bool { return connectedHosts() == 1 }, "agent should connect")

	stopped := make(chan struct{})
	go func() {
		cm.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("turning cluster mode off did not return while an agent was connected")
	}
	waitUntil(t, 5*time.Second, func() bool { return !ag.Connected() }, "agent should see its stream end")

	if err := cm.Start(); err != nil {
		t.Fatalf("turning cluster mode back on failed: %v", err)
	}
	waitUntil(t, 20*time.Second, func() bool { return connectedHosts() == 1 }, "agent should reconnect after cluster mode is turned back on")
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}
