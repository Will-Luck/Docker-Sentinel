package web

import (
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// The container page must describe the scope the checker applies. A fresh
// install has no saved version scope, and the checker treats that as strict.
func TestGlobalVersionScope_UnsetIsStrict(t *testing.T) {
	tests := []struct {
		name   string
		stored map[string]string
		want   string
		label  string
	}{
		{"unset", map[string]string{}, "strict", "strict"},
		{"strict", map[string]string{store.SettingVersionScope: "strict"}, "strict", "strict"},
		{"relaxed", map[string]string{store.SettingVersionScope: "default"}, "default", "relaxed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ss := newMockSettingsStore()
			for k, v := range tt.stored {
				ss.data[k] = v
			}
			srv := &Server{deps: Dependencies{SettingsStore: ss}}
			got := srv.globalVersionScope()
			if got != tt.want {
				t.Fatalf("globalVersionScope() = %q, want %q", got, tt.want)
			}
			if label := effectiveScopeLabel(docker.ScopeDefault, got); label != tt.label {
				t.Errorf("effectiveScopeLabel = %q, want %q", label, tt.label)
			}
		})
	}
}

func TestGlobalVersionScope_NoStoreIsStrict(t *testing.T) {
	srv := &Server{}
	if got := srv.globalVersionScope(); got != "strict" {
		t.Errorf("globalVersionScope() with no store = %q, want strict", got)
	}
}
