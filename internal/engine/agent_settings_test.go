package engine

import (
	"testing"
	"time"
)

// The settings the hub sends its agents are the ones its own updates use:
// a value saved in the dashboard wins as it does locally.
func TestAgentSettingsAreTheHubsOwn(t *testing.T) {
	u, _ := newTestUpdater(t, newMockDocker())
	u.SetSettingsReader(u.store)
	u.cfg.SetImageCleanup(false)
	u.cfg.SetHooksEnabled(true)
	u.cfg.SetDependencyAware(false)
	u.cfg.SetGracePeriod(45 * time.Second)
	for k, v := range map[string]string{"remove_volumes": "true", "image_backup": "true", "rollback_policy": "pinned"} {
		if err := u.store.SaveSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}

	got := u.AgentSettings()
	if got.ImageCleanup || !got.HooksEnabled || got.DependencyAware || !got.RemoveVolumes || !got.ImageBackup ||
		got.RollbackPolicy != "pinned" || got.GracePeriod != 45*time.Second {
		t.Errorf("agent settings = %+v, want the hub's resolved settings", got)
	}
	if (&Updater{}).AgentSettings() != (AgentSettings{}) {
		t.Error("an Updater without config gave settings")
	}
}
