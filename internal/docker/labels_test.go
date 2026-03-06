package docker

import (
	"testing"
	"time"
)

func TestContainerPolicy(t *testing.T) {
	tests := []struct {
		name          string
		labels        map[string]string
		defaultPolicy string
		want          Policy
		wantLabel     bool
	}{
		{"no label, default manual", map[string]string{}, "manual", PolicyManual, false},
		{"no label, default auto", map[string]string{}, "auto", PolicyAuto, false},
		{"explicit auto", map[string]string{"sentinel.policy": "auto"}, "manual", PolicyAuto, true},
		{"explicit manual", map[string]string{"sentinel.policy": "manual"}, "auto", PolicyManual, true},
		{"explicit pinned", map[string]string{"sentinel.policy": "pinned"}, "auto", PolicyPinned, true},
		{"case insensitive", map[string]string{"sentinel.policy": "AUTO"}, "manual", PolicyAuto, true},
		{"invalid label falls back", map[string]string{"sentinel.policy": "yolo"}, "manual", PolicyManual, false},
		{"other labels ignored", map[string]string{"com.example.foo": "bar"}, "manual", PolicyManual, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, fromLabel := ContainerPolicy(tt.labels, tt.defaultPolicy)
			if got != tt.want {
				t.Errorf("ContainerPolicy() policy = %q, want %q", got, tt.want)
			}
			if fromLabel != tt.wantLabel {
				t.Errorf("ContainerPolicy() fromLabel = %v, want %v", fromLabel, tt.wantLabel)
			}
		})
	}
}

func TestContainerSemverScope(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   SemverScope
	}{
		{"no label", map[string]string{}, ScopeDefault},
		{"patch", map[string]string{"sentinel.semver": "patch"}, ScopePatch},
		{"minor", map[string]string{"sentinel.semver": "minor"}, ScopeMinor},
		{"major", map[string]string{"sentinel.semver": "major"}, ScopeMajor},
		{"all alias", map[string]string{"sentinel.semver": "all"}, ScopeMajor},
		{"case insensitive", map[string]string{"sentinel.semver": "MINOR"}, ScopeMinor},
		{"invalid falls back", map[string]string{"sentinel.semver": "yolo"}, ScopeDefault},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerSemverScope(tt.labels)
			if got != tt.want {
				t.Errorf("ContainerSemverScope() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContainerGracePeriod(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   time.Duration
	}{
		{"missing label", map[string]string{}, 0},
		{"empty value", map[string]string{"sentinel.grace-period": ""}, 0},
		{"valid seconds", map[string]string{"sentinel.grace-period": "30s"}, 30 * time.Second},
		{"valid minutes", map[string]string{"sentinel.grace-period": "5m"}, 5 * time.Minute},
		{"valid days (capped)", map[string]string{"sentinel.grace-period": "1d"}, time.Hour},
		{"invalid value", map[string]string{"sentinel.grace-period": "abc"}, 0},
		{"exceeds cap", map[string]string{"sentinel.grace-period": "2h"}, time.Hour},
		{"negative", map[string]string{"sentinel.grace-period": "-5s"}, 0},
		{"exactly 1h", map[string]string{"sentinel.grace-period": "1h"}, time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerGracePeriod(tt.labels)
			if got != tt.want {
				t.Errorf("ContainerGracePeriod() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsLocalImage(t *testing.T) {
	tests := []struct {
		imageRef string
		want     bool
	}{
		// Nothing is "local" — we always try the registry check.
		// The registry checker handles failures gracefully.
		{"nginx", false},
		{"nginx:latest", false},
		{"myapp:v1", false},
		{"library/nginx", false},
		{"ghcr.io/owner/image", false},
		{"docker.io/library/nginx", false},
		{"registry.example.com/myapp:latest", false},
		{"registry.local:5000/myapp", false},
		{"localhost/myapp", false},
		{"gitea/gitea:latest", false},
		{"postgres:16-alpine", false},
	}

	for _, tt := range tests {
		t.Run(tt.imageRef, func(t *testing.T) {
			got := IsLocalImage(tt.imageRef)
			if got != tt.want {
				t.Errorf("IsLocalImage(%q) = %v, want %v", tt.imageRef, got, tt.want)
			}
		})
	}
}

func TestContainerSchedule(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"no label", map[string]string{}, ""},
		{"empty value", map[string]string{"sentinel.schedule": ""}, ""},
		{"cron expression", map[string]string{"sentinel.schedule": "0 3 * * *"}, "0 3 * * *"},
		{"every 6 hours", map[string]string{"sentinel.schedule": "0 */6 * * *"}, "0 */6 * * *"},
		{"other labels ignored", map[string]string{"com.example.schedule": "daily"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerSchedule(tt.labels)
			if got != tt.want {
				t.Errorf("ContainerSchedule() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContainerPullOnly(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"no label", map[string]string{}, false},
		{"empty value", map[string]string{"sentinel.pull-only": ""}, false},
		{"true lowercase", map[string]string{"sentinel.pull-only": "true"}, true},
		{"true uppercase", map[string]string{"sentinel.pull-only": "TRUE"}, true},
		{"true mixed case", map[string]string{"sentinel.pull-only": "True"}, true},
		{"false", map[string]string{"sentinel.pull-only": "false"}, false},
		{"invalid value", map[string]string{"sentinel.pull-only": "yes"}, false},
		{"numeric 1", map[string]string{"sentinel.pull-only": "1"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerPullOnly(tt.labels)
			if got != tt.want {
				t.Errorf("ContainerPullOnly() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContainerRemoveVolumes(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"no label", map[string]string{}, false},
		{"empty value", map[string]string{"sentinel.remove-volumes": ""}, false},
		{"true lowercase", map[string]string{"sentinel.remove-volumes": "true"}, true},
		{"true uppercase", map[string]string{"sentinel.remove-volumes": "TRUE"}, true},
		{"true mixed case", map[string]string{"sentinel.remove-volumes": "True"}, true},
		{"false", map[string]string{"sentinel.remove-volumes": "false"}, false},
		{"invalid value", map[string]string{"sentinel.remove-volumes": "yes"}, false},
		{"numeric 1", map[string]string{"sentinel.remove-volumes": "1"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerRemoveVolumes(tt.labels)
			if got != tt.want {
				t.Errorf("ContainerRemoveVolumes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContainerNotifySnooze(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   time.Duration
	}{
		{"no label", map[string]string{}, 0},
		{"empty value", map[string]string{"sentinel.notify-snooze": ""}, 0},
		{"valid hours", map[string]string{"sentinel.notify-snooze": "6h"}, 6 * time.Hour},
		{"valid minutes", map[string]string{"sentinel.notify-snooze": "30m"}, 30 * time.Minute},
		{"valid days", map[string]string{"sentinel.notify-snooze": "7d"}, 7 * 24 * time.Hour},
		{"one day", map[string]string{"sentinel.notify-snooze": "1d"}, 24 * time.Hour},
		{"invalid value", map[string]string{"sentinel.notify-snooze": "abc"}, 0},
		{"invalid day value", map[string]string{"sentinel.notify-snooze": "xd"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerNotifySnooze(tt.labels)
			if got != tt.want {
				t.Errorf("ContainerNotifySnooze() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContainerUpdateDelay(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   time.Duration
	}{
		{"no label", map[string]string{}, 0},
		{"empty value", map[string]string{"sentinel.delay": ""}, 0},
		{"valid hours", map[string]string{"sentinel.delay": "24h"}, 24 * time.Hour},
		{"valid minutes", map[string]string{"sentinel.delay": "15m"}, 15 * time.Minute},
		{"valid days", map[string]string{"sentinel.delay": "3d"}, 3 * 24 * time.Hour},
		{"invalid value", map[string]string{"sentinel.delay": "abc"}, 0},
		{"invalid day value", map[string]string{"sentinel.delay": "xd"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerUpdateDelay(tt.labels)
			if got != tt.want {
				t.Errorf("ContainerUpdateDelay() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContainerTagFilters(t *testing.T) {
	tests := []struct {
		name        string
		labels      map[string]string
		wantInclude string
		wantExclude string
	}{
		{"no labels", map[string]string{}, "", ""},
		{"include only", map[string]string{"sentinel.include-tags": "^v\\d+"}, "^v\\d+", ""},
		{"exclude only", map[string]string{"sentinel.exclude-tags": ".*-rc.*"}, "", ".*-rc.*"},
		{"both set", map[string]string{
			"sentinel.include-tags": "^v\\d+",
			"sentinel.exclude-tags": ".*-beta.*",
		}, "^v\\d+", ".*-beta.*"},
		{"empty values", map[string]string{
			"sentinel.include-tags": "",
			"sentinel.exclude-tags": "",
		}, "", ""},
		{"other labels ignored", map[string]string{"com.example.tags": "foo"}, "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotInclude, gotExclude := ContainerTagFilters(tt.labels)
			if gotInclude != tt.wantInclude {
				t.Errorf("ContainerTagFilters() include = %q, want %q", gotInclude, tt.wantInclude)
			}
			if gotExclude != tt.wantExclude {
				t.Errorf("ContainerTagFilters() exclude = %q, want %q", gotExclude, tt.wantExclude)
			}
		})
	}
}

func TestParseDurationWithDays(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"standard seconds", "30s", 30 * time.Second, false},
		{"standard minutes", "5m", 5 * time.Minute, false},
		{"standard hours", "2h", 2 * time.Hour, false},
		{"combined h+m", "1h30m", time.Hour + 30*time.Minute, false},
		{"one day", "1d", 24 * time.Hour, false},
		{"seven days", "7d", 7 * 24 * time.Hour, false},
		{"thirty days", "30d", 30 * 24 * time.Hour, false},
		{"zero days", "0d", 0, false},
		{"non-integer days", "1.5d", 0, true},
		{"empty string", "", 0, true},
		{"invalid string", "abc", 0, true},
		{"invalid day prefix", "xd", 0, true},
		{"negative days", "-1d", -24 * time.Hour, false},
		{"negative standard", "-5m", -5 * time.Minute, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDurationWithDays(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseDurationWithDays(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseDurationWithDays(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
