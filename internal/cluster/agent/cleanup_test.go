package agent

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// After a remote update the old image is removed when image cleanup is
// on, as it is after a local update, and kept when it is off or another
// container still runs it.
func TestRemoteUpdate_ImageCleanup(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cleanup   bool
		otherUser bool
		removed   bool
	}{
		{"cleanup on", true, false, true},
		{"cleanup off", false, false, false},
		{"image still in use", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, old, _ := recreateFixture(t)
			if tc.otherUser {
				h.addContainer("web-twin", "nginx:1", true)
			}
			a := newSwapAgent(t, h)
			a.settings.SetImageCleanup(tc.cleanup)

			if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); err != nil {
				t.Fatalf("recreateContainer: %v", err)
			}
			if got := slices.Contains(h.removedImages, old.imageID); got != tc.removed {
				t.Errorf("old image removed = %v, want %v (removed %v)", got, tc.removed, h.removedImages)
			}
		})
	}
}

// With image backup on, the old image is tagged as a backup before the
// pull, as the hub does.
func TestRemoteUpdate_ImageBackup(t *testing.T) {
	h, _, _ := recreateFixture(t)
	a := newSwapAgent(t, h)
	a.settings.SetImageBackup(true)
	a.settings.SetImageCleanup(false)

	if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); err != nil {
		t.Fatalf("recreateContainer: %v", err)
	}
	if len(h.tags) != 1 || !strings.HasPrefix(h.tags[0], "nginx:1 nginx:sentinel-backup-") {
		t.Errorf("tags = %q, want nginx:1 tagged as a sentinel backup", h.tags)
	}
}
