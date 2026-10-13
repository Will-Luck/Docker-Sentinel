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
