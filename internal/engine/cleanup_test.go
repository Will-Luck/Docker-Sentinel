package engine

import (
	"context"
	"fmt"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestCleanupRemovesUnusedImage(t *testing.T) {
	mock := newMockDocker()
	// No containers use the old image.
	mock.containers = []container.Summary{
		{ID: "c1", Names: []string{"/nginx"}},
	}
	mock.inspectResults["c1"] = container.InspectResponse{
		ID:    "c1",
		Image: "sha256:newimage",
	}

	u, _ := newTestUpdater(t, mock)
	u.cleanupOldImage(context.Background(), "sha256:oldimage", "nginx")

	if len(mock.removeImageCalls) != 1 {
		t.Fatalf("removeImageCalls = %d, want 1", len(mock.removeImageCalls))
	}
	if mock.removeImageCalls[0] != "sha256:oldimage" {
		t.Errorf("removed image = %q, want sha256:oldimage", mock.removeImageCalls[0])
	}
}

func TestCleanupKeepsImageInUse(t *testing.T) {
	mock := newMockDocker()
	// Another container still uses the old image.
	mock.containers = []container.Summary{
		{ID: "c1", Names: []string{"/nginx"}},
		{ID: "c2", Names: []string{"/other"}},
	}
	mock.inspectResults["c1"] = container.InspectResponse{
		ID:    "c1",
		Image: "sha256:newimage",
	}
	mock.inspectResults["c2"] = container.InspectResponse{
		ID:    "c2",
		Image: "sha256:oldimage", // still using old image
	}

	u, _ := newTestUpdater(t, mock)
	u.cleanupOldImage(context.Background(), "sha256:oldimage", "nginx")

	if len(mock.removeImageCalls) != 0 {
		t.Errorf("removeImageCalls = %d, want 0 (image in use)", len(mock.removeImageCalls))
	}
}

func TestCleanupDisabledByConfig(t *testing.T) {
	mock := newMockDocker()
	u, _ := newTestUpdater(t, mock)
	u.cfg.SetImageCleanup(false)

	u.cleanupOldImage(context.Background(), "sha256:oldimage", "nginx")

	if len(mock.removeImageCalls) != 0 {
		t.Errorf("removeImageCalls = %d, want 0 (feature disabled)", len(mock.removeImageCalls))
	}
}

func TestCleanupRemoveImageErrorLogged(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{}
	mock.removeImageErr["sha256:oldimage"] = fmt.Errorf("image in use by stopped container")

	u, _ := newTestUpdater(t, mock)
	// Should not panic, just log the error.
	u.cleanupOldImage(context.Background(), "sha256:oldimage", "nginx")

	if len(mock.removeImageCalls) != 1 {
		t.Fatalf("removeImageCalls = %d, want 1 (attempted removal)", len(mock.removeImageCalls))
	}
}
