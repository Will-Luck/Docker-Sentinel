package engine

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
)

// update_available carried no new_image, so a template's {{.NewImage}} was
// empty. It is the target image: the newer tag, else the same tag.
func TestUpdateAvailableCarriesNewImage(t *testing.T) {
	t.Run("remote same tag", func(t *testing.T) {
		u, _, _ := remotePolicyFixture(t, map[string]string{"sentinel.policy": "manual"})
		cn := &capturingNotifier{}
		u.notifier = notify.NewMulti(u.log, cn)
		u.Scan(context.Background(), ScanScheduled)
		cn.mu.Lock()
		defer cn.mu.Unlock()
		for _, e := range cn.events {
			if e.Type == notify.EventUpdateAvailable {
				if e.NewImage != "nginx:latest" {
					t.Errorf("remote NewImage = %q, want nginx:latest", e.NewImage)
				}
				return
			}
		}
		t.Fatalf("no update_available sent: %v", cn.events)
	})
	t.Run("local same tag", func(t *testing.T) {
		mock := newMockDocker()
		mock.containers = []container.Summary{{ID: "aaa", Names: []string{"/myapp"}, Image: "fake.local/app:1.0"}}
		mock.imageDigests["fake.local/app:1.0"] = "fake.local/app@sha256:old"
		mock.distDigests["fake.local/app:1.0"] = "sha256:new"
		u, _ := newTestUpdater(t, mock)
		cn := &capturingNotifier{}
		u.notifier = notify.NewMulti(u.log, cn)
		u.Scan(context.Background(), ScanScheduled)
		cn.mu.Lock()
		defer cn.mu.Unlock()
		for _, e := range cn.events {
			if e.Type == notify.EventUpdateAvailable {
				if e.NewImage != "fake.local/app:1.0" {
					t.Errorf("local NewImage = %q", e.NewImage)
				}
				return
			}
		}
		t.Fatalf("no update_available sent: %v", cn.events)
	})
}
