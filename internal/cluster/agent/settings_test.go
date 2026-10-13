package agent

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
)

// The agent follows the update settings the hub sends, except those its
// own environment sets, which win on that host.
func TestHandleSettingsSync_FollowsTheHubUnlessTheEnvSetsIt(t *testing.T) {
	a := newTestAgent(t.TempDir(), newMockDocker())
	a.settings = config.NewTestConfig() // image cleanup on, the rest off
	a.cfg.EnvSettings = map[string]bool{"image_cleanup": true}

	a.handleSettingsSync(&proto.SettingsSync{
		ImageCleanup:    false,
		RemoveVolumes:   true,
		ImageBackup:     true,
		HooksEnabled:    true,
		DependencyAware: false,
		GracePeriod:     durationpb.New(time.Minute),
	})

	s := a.settings
	if !s.ImageCleanup() {
		t.Error("image cleanup turned off by the hub although the agent's environment sets it")
	}
	if !s.RemoveVolumes() || !s.ImageBackup() || !s.HooksEnabled() || s.DependencyAware() {
		t.Errorf("settings = remove volumes %v, image backup %v, hooks %v, dependency aware %v; want the hub's",
			s.RemoveVolumes(), s.ImageBackup(), s.HooksEnabled(), s.DependencyAware())
	}
}

// The hub's settings are kept on disk: an agent that restarts while the hub
// is away follows them at once. A cache written before any SettingsSync
// (older hubs never send one) changes nothing.
func TestLoadPolicyCache_AppliesTheHubsSettings(t *testing.T) {
	dir := t.TempDir()
	first := newTestAgent(dir, newMockDocker())
	first.handleSettingsSync(&proto.SettingsSync{ImageCleanup: false, HooksEnabled: true, RemoveVolumes: true, DependencyAware: true})

	again := newTestAgent(dir, newMockDocker())
	again.settings = config.NewTestConfig()
	if err := again.loadPolicyCache(); err != nil {
		t.Fatal(err)
	}
	if again.settings.ImageCleanup() || !again.settings.HooksEnabled() || !again.settings.RemoveVolumes() {
		t.Errorf("after a restart: image cleanup %v, hooks %v, remove volumes %v; want the hub's last settings",
			again.settings.ImageCleanup(), again.settings.HooksEnabled(), again.settings.RemoveVolumes())
	}

	other := newTestAgent(t.TempDir(), newMockDocker())
	other.handlePolicySync(&proto.PolicySync{DefaultPolicy: "auto"})
	fresh := newTestAgent(other.cfg.DataDir, newMockDocker())
	fresh.settings = config.NewTestConfig()
	if err := fresh.loadPolicyCache(); err != nil {
		t.Fatal(err)
	}
	if !fresh.settings.ImageCleanup() || !fresh.settings.DependencyAware() {
		t.Error("a cache with no settings from the hub turned settings off")
	}
}

// The grace period an agent update waits before it validates the new
// container is the hub's, unless SENTINEL_GRACE_PERIOD is set on the agent.
func TestHandleSettingsSync_GracePeriod(t *testing.T) {
	a := newTestAgent(t.TempDir(), newMockDocker())
	a.handleSettingsSync(&proto.SettingsSync{GracePeriod: durationpb.New(45 * time.Second)})
	if got := a.settings.GracePeriod(); got != 45*time.Second {
		t.Errorf("grace period = %v, want the hub's 45s", got)
	}

	pinned := newTestAgent(t.TempDir(), newMockDocker())
	pinned.settings.SetGracePeriod(10 * time.Second)
	pinned.cfg.EnvSettings = map[string]bool{"grace_period": true}
	pinned.handleSettingsSync(&proto.SettingsSync{GracePeriod: durationpb.New(45 * time.Second)})
	if got := pinned.settings.GracePeriod(); got != 10*time.Second {
		t.Errorf("grace period = %v, want the agent's own 10s", got)
	}
}

// smoke F8: clearing the hub's rollback policy ("No change") never reached
// agents: an empty value was taken as "not sent". A SettingsSync carries
// every setting, so an empty rollback policy clears it.
func TestHandleSettingsSync_ClearsTheRollbackPolicy(t *testing.T) {
	a := newTestAgent(t.TempDir(), newMockDocker())
	a.handleSettingsSync(&proto.SettingsSync{RollbackPolicy: "manual"})
	a.handleSettingsSync(&proto.SettingsSync{RollbackPolicy: ""})
	if got := a.policies.rollbackPolicy; got != "" {
		t.Errorf("rollback policy = %q after the hub cleared it, want empty", got)
	}
	again := newTestAgent(a.cfg.DataDir, newMockDocker())
	if err := again.loadPolicyCache(); err != nil {
		t.Fatal(err)
	}
	if got := again.policies.rollbackPolicy; got != "" {
		t.Errorf("cached rollback policy = %q, want empty", got)
	}
}
