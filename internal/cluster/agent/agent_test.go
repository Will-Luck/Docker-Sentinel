package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// ---------------------------------------------------------------------------
// Mock Docker API
// ---------------------------------------------------------------------------

// mockDocker implements DockerAPI for agent tests. Intentionally minimal —
// only the methods the agent actually exercises are wired up.
type mockDocker struct {
	mu sync.Mutex

	containers    []container.Summary
	containersErr error

	allContainers    []container.Summary
	allContainersErr error

	inspectResults map[string]container.InspectResponse
	inspectErr     map[string]error

	stopCalls []string
	stopErr   map[string]error

	removeCalls []string
	removeErr   map[string]error

	createResult map[string]string // name -> id
	createErr    map[string]error

	startCalls []string
	startErr   map[string]error

	restartCalls []string
	restartErr   map[string]error

	pullCalls []string
	pullErr   map[string]error

	imageDigests   map[string]string
	imageDigestErr map[string]error

	execResults map[string]struct {
		exitCode int
		output   string
	}
	execErr map[string]error

	logResults map[string]string
	logErr     map[string]error
}

func newMockDocker() *mockDocker {
	return &mockDocker{
		inspectResults: make(map[string]container.InspectResponse),
		inspectErr:     make(map[string]error),
		stopErr:        make(map[string]error),
		removeErr:      make(map[string]error),
		createResult:   make(map[string]string),
		createErr:      make(map[string]error),
		startErr:       make(map[string]error),
		restartErr:     make(map[string]error),
		pullErr:        make(map[string]error),
		imageDigests:   make(map[string]string),
		imageDigestErr: make(map[string]error),
		execResults: make(map[string]struct {
			exitCode int
			output   string
		}),
		execErr:    make(map[string]error),
		logResults: make(map[string]string),
		logErr:     make(map[string]error),
	}
}

func (m *mockDocker) ListContainers(_ context.Context) ([]container.Summary, error) {
	return m.containers, m.containersErr
}

func (m *mockDocker) ListAllContainers(_ context.Context) ([]container.Summary, error) {
	if m.allContainers != nil || m.allContainersErr != nil {
		return m.allContainers, m.allContainersErr
	}
	return m.containers, m.containersErr
}

func (m *mockDocker) InspectContainer(_ context.Context, id string) (container.InspectResponse, error) {
	if err, ok := m.inspectErr[id]; ok && err != nil {
		return container.InspectResponse{}, err
	}
	return m.inspectResults[id], nil
}

func (m *mockDocker) StopContainer(_ context.Context, id string, _ int) error {
	m.mu.Lock()
	m.stopCalls = append(m.stopCalls, id)
	m.mu.Unlock()
	if err, ok := m.stopErr[id]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) RemoveContainer(_ context.Context, id string) error {
	m.mu.Lock()
	m.removeCalls = append(m.removeCalls, id)
	m.mu.Unlock()
	if err, ok := m.removeErr[id]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) CreateContainer(_ context.Context, name string, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.createErr[name]; ok {
		return "", err
	}
	if id, ok := m.createResult[name]; ok {
		return id, nil
	}
	return "new-" + name, nil
}

func (m *mockDocker) StartContainer(_ context.Context, id string) error {
	m.mu.Lock()
	m.startCalls = append(m.startCalls, id)
	m.mu.Unlock()
	if err, ok := m.startErr[id]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) RestartContainer(_ context.Context, id string) error {
	m.mu.Lock()
	m.restartCalls = append(m.restartCalls, id)
	m.mu.Unlock()
	if err, ok := m.restartErr[id]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) PullImage(_ context.Context, ref string) error {
	m.mu.Lock()
	m.pullCalls = append(m.pullCalls, ref)
	m.mu.Unlock()
	if err, ok := m.pullErr[ref]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) ImageDigest(_ context.Context, ref string) (string, error) {
	if err, ok := m.imageDigestErr[ref]; ok {
		return "", err
	}
	return m.imageDigests[ref], nil
}

func (m *mockDocker) ExecContainer(_ context.Context, id string, _ []string, _ int) (int, string, error) {
	if err, ok := m.execErr[id]; ok {
		return -1, "", err
	}
	if r, ok := m.execResults[id]; ok {
		return r.exitCode, r.output, nil
	}
	return 0, "", nil
}

func (m *mockDocker) ContainerLogs(_ context.Context, id string, _ int) (string, error) {
	if err, ok := m.logErr[id]; ok {
		return "", err
	}
	return m.logResults[id], nil
}

// ---------------------------------------------------------------------------
// Policy Resolution
// ---------------------------------------------------------------------------

func TestResolvePolicyLabelTakesPrecedence(t *testing.T) {
	pc := newPolicyCache()
	pc.policies["nginx"] = "auto"
	pc.defaultPolicy = "pinned"

	// Container label should override both the per-container policy and default.
	labels := map[string]string{"sentinel.policy": "manual"}
	got := pc.resolvePolicy("nginx", labels)
	if got != "manual" {
		t.Errorf("resolvePolicy() = %q, want %q", got, "manual")
	}
}

func TestResolvePolicyPerContainerOverride(t *testing.T) {
	pc := newPolicyCache()
	pc.policies["redis"] = "pinned"
	pc.defaultPolicy = "auto"

	// No label — should use the server-pushed per-container override.
	got := pc.resolvePolicy("redis", nil)
	if got != "pinned" {
		t.Errorf("resolvePolicy() = %q, want %q", got, "pinned")
	}
}

func TestResolvePolicyDefaultFallback(t *testing.T) {
	pc := newPolicyCache()
	pc.defaultPolicy = "auto"

	// No label, no per-container override — should use the default.
	got := pc.resolvePolicy("unknown-container", nil)
	if got != "auto" {
		t.Errorf("resolvePolicy() = %q, want %q", got, "auto")
	}
}

func TestResolvePolicyHardcodedFallback(t *testing.T) {
	pc := newPolicyCache()
	pc.defaultPolicy = ""

	// No label, no override, no default — hardcoded "manual" as the
	// safest fallback for autonomous operation.
	got := pc.resolvePolicy("anything", nil)
	if got != "manual" {
		t.Errorf("resolvePolicy() = %q, want %q", got, "manual")
	}
}

func TestApplyPolicySyncFullReplace(t *testing.T) {
	pc := newPolicyCache()
	pc.policies["old-app"] = "auto"

	// PolicySync should fully replace the policy map.
	pc.applyPolicySync(&proto.PolicySync{
		Policies: map[string]string{
			"nginx": "pinned",
			"redis": "manual",
		},
		DefaultPolicy: "auto",
	})

	// Old entry should be gone.
	if _, ok := pc.policies["old-app"]; ok {
		t.Error("expected old-app to be removed after full replace")
	}
	if pc.policies["nginx"] != "pinned" {
		t.Errorf("nginx = %q, want %q", pc.policies["nginx"], "pinned")
	}
	if pc.policies["redis"] != "manual" {
		t.Errorf("redis = %q, want %q", pc.policies["redis"], "manual")
	}
	if pc.defaultPolicy != "auto" {
		t.Errorf("defaultPolicy = %q, want %q", pc.defaultPolicy, "auto")
	}
}

func TestApplyPolicySyncEmptyPoliciesPreservesExisting(t *testing.T) {
	pc := newPolicyCache()
	pc.policies["app"] = "pinned"

	// Sync with nil Policies should not wipe the existing map.
	pc.applyPolicySync(&proto.PolicySync{
		DefaultPolicy: "manual",
	})

	if pc.policies["app"] != "pinned" {
		t.Errorf("expected existing policy to survive nil sync, got %q", pc.policies["app"])
	}
	if pc.defaultPolicy != "manual" {
		t.Errorf("defaultPolicy = %q, want %q", pc.defaultPolicy, "manual")
	}
}

func TestApplySettingsSync(t *testing.T) {
	pc := newPolicyCache()

	pc.applySettingsSync(&proto.SettingsSync{
		ImageCleanup:    true,
		HooksEnabled:    true,
		DependencyAware: true,
		RollbackPolicy:  "automatic",
	})

	if !pc.imageCleanup {
		t.Error("expected imageCleanup = true")
	}
	if !pc.hooksEnabled {
		t.Error("expected hooksEnabled = true")
	}
	if !pc.dependencyAware {
		t.Error("expected dependencyAware = true")
	}
	if pc.rollbackPolicy != "automatic" {
		t.Errorf("rollbackPolicy = %q, want %q", pc.rollbackPolicy, "automatic")
	}
}

// ---------------------------------------------------------------------------
// Policy Cache Persistence (save/load round-trip)
// ---------------------------------------------------------------------------

func TestPolicyCacheSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	a := newTestAgent(dir, newMockDocker())

	// Set up some policies and settings.
	a.policies.policies["web"] = "auto"
	a.policies.policies["db"] = "pinned"
	a.policies.defaultPolicy = "manual"
	a.policies.pollInterval = 3 * time.Hour
	a.policies.rollbackPolicy = "automatic"
	a.policies.hooksEnabled = true

	if err := a.savePolicyCache(); err != nil {
		t.Fatalf("savePolicyCache: %v", err)
	}

	// Verify the file was created.
	path := filepath.Join(dir, policyCacheFilename)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("policy cache file not found: %v", err)
	}

	// Create a fresh agent and load the cache.
	a2 := newTestAgent(dir, newMockDocker())
	if err := a2.loadPolicyCache(); err != nil {
		t.Fatalf("loadPolicyCache: %v", err)
	}

	if a2.policies.policies["web"] != "auto" {
		t.Errorf("web = %q, want %q", a2.policies.policies["web"], "auto")
	}
	if a2.policies.policies["db"] != "pinned" {
		t.Errorf("db = %q, want %q", a2.policies.policies["db"], "pinned")
	}
	if a2.policies.defaultPolicy != "manual" {
		t.Errorf("defaultPolicy = %q, want %q", a2.policies.defaultPolicy, "manual")
	}
	if a2.policies.pollInterval != 3*time.Hour {
		t.Errorf("pollInterval = %v, want %v", a2.policies.pollInterval, 3*time.Hour)
	}
	if a2.policies.rollbackPolicy != "automatic" {
		t.Errorf("rollbackPolicy = %q, want %q", a2.policies.rollbackPolicy, "automatic")
	}
	if !a2.policies.hooksEnabled {
		t.Error("expected hooksEnabled = true after load")
	}
}

func TestLoadPolicyCacheMissingFile(t *testing.T) {
	dir := t.TempDir()
	a := newTestAgent(dir, newMockDocker())

	// Should be a no-op — not an error.
	if err := a.loadPolicyCache(); err != nil {
		t.Fatalf("loadPolicyCache with missing file: %v", err)
	}

	// Default policy should remain "manual".
	if a.policies.defaultPolicy != "manual" {
		t.Errorf("defaultPolicy = %q, want %q", a.policies.defaultPolicy, "manual")
	}
}

// ---------------------------------------------------------------------------
// Journal
// ---------------------------------------------------------------------------

func TestJournalAddAndEntries(t *testing.T) {
	dir := t.TempDir()
	j, err := newJournal(dir)
	if err != nil {
		t.Fatalf("newJournal: %v", err)
	}

	if j.Len() != 0 {
		t.Errorf("expected empty journal, got %d entries", j.Len())
	}

	entry := cluster.JournalEntry{
		ID:        "j-001",
		Action:    "update",
		Container: "nginx",
		OldImage:  "nginx:1.24",
		NewImage:  "nginx:1.25",
		Outcome:   "success",
		Duration:  5 * time.Second,
	}
	if err := j.Add(entry); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if j.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d", j.Len())
	}

	entries := j.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ID != "j-001" {
		t.Errorf("ID = %q, want %q", entries[0].ID, "j-001")
	}
	if entries[0].Container != "nginx" {
		t.Errorf("Container = %q, want %q", entries[0].Container, "nginx")
	}
	if entries[0].Timestamp.IsZero() {
		t.Error("expected auto-set timestamp, got zero")
	}
}

func TestJournalAutoTimestamp(t *testing.T) {
	dir := t.TempDir()
	j, err := newJournal(dir)
	if err != nil {
		t.Fatal(err)
	}

	before := time.Now().UTC()
	if err := j.Add(cluster.JournalEntry{ID: "ts-test", Action: "hook"}); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()

	entries := j.Entries()
	ts := entries[0].Timestamp
	if ts.Before(before) || ts.After(after) {
		t.Errorf("auto timestamp %v not between %v and %v", ts, before, after)
	}
}

func TestJournalExplicitTimestamp(t *testing.T) {
	dir := t.TempDir()
	j, err := newJournal(dir)
	if err != nil {
		t.Fatal(err)
	}

	explicit := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	if err := j.Add(cluster.JournalEntry{
		ID:        "ts-explicit",
		Timestamp: explicit,
		Action:    "update",
	}); err != nil {
		t.Fatal(err)
	}

	entries := j.Entries()
	if !entries[0].Timestamp.Equal(explicit) {
		t.Errorf("timestamp = %v, want %v", entries[0].Timestamp, explicit)
	}
}

func TestJournalClear(t *testing.T) {
	dir := t.TempDir()
	j, err := newJournal(dir)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if err := j.Add(cluster.JournalEntry{
			ID:     fmt.Sprintf("j-%d", i),
			Action: "update",
		}); err != nil {
			t.Fatal(err)
		}
	}

	if j.Len() != 3 {
		t.Fatalf("expected 3 entries before clear, got %d", j.Len())
	}

	if err := j.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	if j.Len() != 0 {
		t.Errorf("expected 0 entries after clear, got %d", j.Len())
	}

	// File should be removed.
	if _, err := os.Stat(filepath.Join(dir, journalFilename)); !os.IsNotExist(err) {
		t.Error("expected journal file to be removed after Clear")
	}
}

func TestJournalPersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	// First journal writes entries.
	j1, err := newJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := j1.Add(cluster.JournalEntry{ID: "persist-1", Action: "update", Container: "app"}); err != nil {
		t.Fatal(err)
	}
	if err := j1.Add(cluster.JournalEntry{ID: "persist-2", Action: "hook", Container: "db"}); err != nil {
		t.Fatal(err)
	}

	// Simulate restart — new journal should load existing entries from disk.
	j2, err := newJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if j2.Len() != 2 {
		t.Fatalf("expected 2 entries after reload, got %d", j2.Len())
	}

	entries := j2.Entries()
	if entries[0].ID != "persist-1" {
		t.Errorf("first entry ID = %q, want %q", entries[0].ID, "persist-1")
	}
	if entries[1].Container != "db" {
		t.Errorf("second entry Container = %q, want %q", entries[1].Container, "db")
	}
}

func TestJournalEntriesReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	j, err := newJournal(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := j.Add(cluster.JournalEntry{ID: "original", Action: "update"}); err != nil {
		t.Fatal(err)
	}

	// Mutating the returned slice must not affect the journal's internal state.
	entries := j.Entries()
	entries[0].ID = "mutated"

	internal := j.Entries()
	if internal[0].ID != "original" {
		t.Errorf("Entries() did not return a copy; internal was mutated to %q", internal[0].ID)
	}
}

// ---------------------------------------------------------------------------
// Dedup
// ---------------------------------------------------------------------------

func TestDedupFirstSeen(t *testing.T) {
	d := newDedup(100)

	// First time should return false (not seen).
	if d.isSeen("req-1") {
		t.Error("expected false for first occurrence")
	}

	// Second time should return true (already seen).
	if !d.isSeen("req-1") {
		t.Error("expected true for second occurrence")
	}
}

func TestDedupDifferentIDs(t *testing.T) {
	d := newDedup(100)

	d.isSeen("req-1")
	if d.isSeen("req-2") {
		t.Error("expected false for different ID")
	}
}

func TestDedupCleanupOnOverflow(t *testing.T) {
	d := newDedup(5)

	// Fill beyond maxSize to trigger cleanup.
	for i := 0; i < 10; i++ {
		d.isSeen(fmt.Sprintf("req-%d", i))
	}

	// After cleanup, very recent entries should still be present.
	// The cleanup removes entries older than 5 minutes, so entries
	// added within the last second should survive.
	if !d.isSeen("req-9") {
		// req-9 was just added, so isSeen should return true.
		t.Error("expected recent entry to survive cleanup")
	}
}

// ---------------------------------------------------------------------------
// Backoff
// ---------------------------------------------------------------------------

func TestBackoffSequence(t *testing.T) {
	bo := newBackoff()

	// Expected sequence: 1s, 2s, 4s, 8s, 16s, 30s (capped).
	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	for i, expected := range want {
		got := bo.next()
		if got != expected {
			t.Errorf("attempt %d: got %v, want %v", i, got, expected)
		}
	}
}

func TestBackoffReset(t *testing.T) {
	bo := newBackoff()

	// Advance a few steps.
	bo.next() // 1s
	bo.next() // 2s
	bo.next() // 4s

	bo.reset()

	// After reset, should start from 1s again.
	got := bo.next()
	if got != 1*time.Second {
		t.Errorf("after reset: got %v, want %v", got, 1*time.Second)
	}
}

// ---------------------------------------------------------------------------
// Autonomous mode decisions
// ---------------------------------------------------------------------------

func TestShouldEnterAutonomousGracePeriodDisabled(t *testing.T) {
	a := newTestAgent(t.TempDir(), newMockDocker())
	a.cfg.GracePeriodOffline = 0 // disabled

	a.mu.Lock()
	a.offlineSince = time.Now().Add(-1 * time.Hour)
	a.mu.Unlock()

	if a.shouldEnterAutonomous() {
		t.Error("expected false when GracePeriodOffline = 0")
	}
}

func TestShouldEnterAutonomousNotOffline(t *testing.T) {
	a := newTestAgent(t.TempDir(), newMockDocker())
	a.cfg.GracePeriodOffline = 5 * time.Minute

	// offlineSince is zero (connected).
	if a.shouldEnterAutonomous() {
		t.Error("expected false when offlineSince is zero")
	}
}

func TestShouldEnterAutonomousWithinGracePeriod(t *testing.T) {
	a := newTestAgent(t.TempDir(), newMockDocker())
	a.cfg.GracePeriodOffline = 30 * time.Minute

	a.mu.Lock()
	a.offlineSince = time.Now().Add(-5 * time.Minute) // only 5 min offline
	a.mu.Unlock()

	if a.shouldEnterAutonomous() {
		t.Error("expected false when offline duration < grace period")
	}
}

func TestShouldEnterAutonomousPastGracePeriod(t *testing.T) {
	a := newTestAgent(t.TempDir(), newMockDocker())
	a.cfg.GracePeriodOffline = 10 * time.Minute

	a.mu.Lock()
	a.offlineSince = time.Now().Add(-15 * time.Minute)
	a.mu.Unlock()

	if !a.shouldEnterAutonomous() {
		t.Error("expected true when offline duration > grace period")
	}
}

// ---------------------------------------------------------------------------
// Container info conversion
// ---------------------------------------------------------------------------

func TestContainerInfoFromSummary(t *testing.T) {
	s := container.Summary{
		ID:    "abc123",
		Names: []string{"/my-nginx"},
		Image: "nginx:1.25",
		State: "running",
		Labels: map[string]string{
			"sentinel.policy": "auto",
		},
		Created: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC).Unix(),
	}

	info := containerInfoFromSummary(&s)
	if info.Name != "my-nginx" {
		t.Errorf("Name = %q, want %q", info.Name, "my-nginx")
	}
	if info.Image != "nginx:1.25" {
		t.Errorf("Image = %q, want %q", info.Image, "nginx:1.25")
	}
	if info.State != "running" {
		t.Errorf("State = %q, want %q", info.State, "running")
	}
	if info.Labels["sentinel.policy"] != "auto" {
		t.Errorf("label sentinel.policy = %q, want %q", info.Labels["sentinel.policy"], "auto")
	}
	if info.Created == nil {
		t.Error("expected non-nil Created timestamp")
	}
}

func TestContainerInfoFromSummaryNoNames(t *testing.T) {
	s := container.Summary{
		ID:    "xyz",
		Image: "redis:7",
		State: "exited",
	}

	info := containerInfoFromSummary(&s)
	if info.Name != "" {
		t.Errorf("Name = %q, want empty", info.Name)
	}
}

func TestContainerInfoPortDedup(t *testing.T) {
	// Docker reports separate IPv4 and IPv6 entries for the same port binding.
	// The conversion should deduplicate them.
	s := container.Summary{
		ID:    "port-test",
		Names: []string{"/port-app"},
		Image: "app:latest",
		State: "running",
		Ports: []container.PortSummary{
			{IP: netip.MustParseAddr("0.0.0.0"), PublicPort: 8080, PrivatePort: 80, Type: "tcp"},
			{IP: netip.MustParseAddr("::"), PublicPort: 8080, PrivatePort: 80, Type: "tcp"},
		},
	}

	info := containerInfoFromSummary(&s)
	if len(info.Ports) != 1 {
		t.Errorf("expected 1 port after dedup, got %d", len(info.Ports))
	}
	if info.Ports[0].HostPort != 8080 {
		t.Errorf("HostPort = %d, want %d", info.Ports[0].HostPort, 8080)
	}
}

func TestContainerInfoSkipsUnpublishedPorts(t *testing.T) {
	s := container.Summary{
		ID:    "exposed-only",
		Names: []string{"/exposed"},
		Image: "app:1.0",
		State: "running",
		Ports: []container.PortSummary{
			{PrivatePort: 3306, Type: "tcp"}, // exposed but not published (PublicPort = 0)
		},
	}

	info := containerInfoFromSummary(&s)
	if len(info.Ports) != 0 {
		t.Errorf("expected 0 ports for unpublished, got %d", len(info.Ports))
	}
}

// ---------------------------------------------------------------------------
// configFromInspect
// ---------------------------------------------------------------------------

func TestConfigFromInspect(t *testing.T) {
	inspect := &container.InspectResponse{
		Config: &container.Config{
			Image: "old:1.0",
			Env:   []string{"FOO=bar"},
		},
		HostConfig: &container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: "always"},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {
					Aliases:   []string{"app"},
					NetworkID: "net-123",
				},
			},
		},
	}

	cfg, hostCfg, netCfg := configFromInspect(inspect, "new:2.0")

	if cfg.Image != "new:2.0" {
		t.Errorf("Image = %q, want %q", cfg.Image, "new:2.0")
	}
	// Original config should be preserved (env).
	if len(cfg.Env) != 1 || cfg.Env[0] != "FOO=bar" {
		t.Errorf("Env = %v, want [FOO=bar]", cfg.Env)
	}
	if hostCfg.RestartPolicy.Name != "always" {
		t.Errorf("RestartPolicy = %q, want %q", hostCfg.RestartPolicy.Name, "always")
	}
	if netCfg.EndpointsConfig["bridge"].NetworkID != "net-123" {
		t.Errorf("NetworkID = %q, want %q", netCfg.EndpointsConfig["bridge"].NetworkID, "net-123")
	}
}

func TestConfigFromInspectNoNetworks(t *testing.T) {
	inspect := &container.InspectResponse{
		Config:     &container.Config{Image: "app:1.0"},
		HostConfig: &container.HostConfig{},
	}

	_, _, netCfg := configFromInspect(inspect, "app:2.0")

	// Should return an empty NetworkingConfig, not nil or panic.
	if netCfg == nil {
		t.Fatal("expected non-nil NetworkingConfig")
	}
	if len(netCfg.EndpointsConfig) != 0 {
		t.Errorf("expected empty EndpointsConfig, got %d entries", len(netCfg.EndpointsConfig))
	}
}

// ---------------------------------------------------------------------------
// Connected / ContainerCount state
// ---------------------------------------------------------------------------

func TestConnectedState(t *testing.T) {
	a := newTestAgent(t.TempDir(), newMockDocker())

	if a.Connected() {
		t.Error("expected false initially")
	}

	a.setConnected()
	if !a.Connected() {
		t.Error("expected true after setConnected")
	}

	a.setOffline()
	if a.Connected() {
		t.Error("expected false after setOffline")
	}
}

func TestSetOfflineTracksTime(t *testing.T) {
	a := newTestAgent(t.TempDir(), newMockDocker())

	before := time.Now()
	a.setOffline()

	a.mu.RLock()
	offlineSince := a.offlineSince
	a.mu.RUnlock()

	if offlineSince.Before(before) {
		t.Error("offlineSince should be set to approximately now")
	}

	// Calling setOffline again should NOT reset the timer.
	a.setOffline()
	a.mu.RLock()
	secondCall := a.offlineSince
	a.mu.RUnlock()

	if !secondCall.Equal(offlineSince) {
		t.Error("second setOffline call should not reset offlineSince")
	}
}

func TestSetConnectedClearsOffline(t *testing.T) {
	a := newTestAgent(t.TempDir(), newMockDocker())

	a.setOffline()
	a.setConnected()

	a.mu.RLock()
	offlineSince := a.offlineSince
	a.mu.RUnlock()

	if !offlineSince.IsZero() {
		t.Error("setConnected should clear offlineSince to zero")
	}
}

// ---------------------------------------------------------------------------
// clampInt32
// ---------------------------------------------------------------------------

func TestClampInt32(t *testing.T) {
	tests := []struct {
		input int
		want  int32
	}{
		{0, 0},
		{255, 255},
		{-1, -1},
		{1<<31 - 1, 1<<31 - 1}, // max int32
		{1 << 31, 1<<31 - 1},   // overflow clamped
		{-1 << 31, -1 << 31},   // min int32
		{-(1 << 32), -1 << 31}, // underflow clamped
	}
	for _, tt := range tests {
		got := clampInt32(tt.input)
		if got != tt.want {
			t.Errorf("clampInt32(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestAgent(dataDir string, docker DockerAPI) *Agent {
	return &Agent{
		cfg: Config{
			DataDir: dataDir,
		},
		docker:   docker,
		log:      noopLogger(),
		dedup:    newDedup(100),
		policies: newPolicyCache(),
	}
}

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Suppress unused import warnings for netip (used in port dedup tests).
var _ = netip.Addr{}
