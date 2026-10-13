package engine

import (
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
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
	for k, v := range map[string]string{"remove_volumes": "true", cluster.SettingRemoveVolumesOnAgents: "true",
		"image_backup": "true", "rollback_policy": "pinned"} {
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

// review (important): the hub's Remove volumes setting reached every agent
// on upgrade, though agents before v3.0.0 never removed volumes: a database
// with only an anonymous volume lost its data on its next remote update.
// Agents get it only when "Remove volumes on agents too" is on as well.
func TestAgentSettings_RemoveVolumesOnlyWhenAppliedToAgents(t *testing.T) {
	u, _ := newTestUpdater(t, newMockDocker())
	u.SetSettingsReader(u.store)
	if err := u.store.SaveSetting("remove_volumes", "true"); err != nil {
		t.Fatal(err)
	}
	if u.AgentSettings().RemoveVolumes {
		t.Error("agents told to remove volumes although the agents setting is off")
	}
	if err := u.store.SaveSetting(cluster.SettingRemoveVolumesOnAgents, "true"); err != nil {
		t.Fatal(err)
	}
	if !u.AgentSettings().RemoveVolumes {
		t.Error("agents not told to remove volumes with both settings on")
	}
	if err := u.store.SaveSetting("remove_volumes", "false"); err != nil {
		t.Fatal(err)
	}
	if u.AgentSettings().RemoveVolumes {
		t.Error("agents told to remove volumes with Remove volumes itself off")
	}
}
