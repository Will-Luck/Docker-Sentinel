package engine

import (
	"context"
	"slices"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
)

// imagesDocker lists a fixed set of images.
type imagesDocker struct {
	*mockDocker
	images []docker.ImageSummary
}

func (d *imagesDocker) ListImages(context.Context) ([]docker.ImageSummary, error) {
	return d.images, nil
}

func backupUpdate(t *testing.T, images []docker.ImageSummary) (*mockDocker, *Updater) {
	t.Helper()
	mock, u := setupUpdateWithScanVerify(t)
	u.docker = &imagesDocker{mockDocker: mock, images: images}
	u.cfg.SetImageCleanup(true)
	u.cfg.SetImageBackup(true)
	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	return mock, u
}

// Image cleanup leaves the old image alone when it carries a backup tag:
// deleting it by ID took the backup with it.
func TestCleanupKeepsBackedUpImage(t *testing.T) {
	mock, _ := backupUpdate(t, []docker.ImageSummary{
		{ID: "sha256:old111", RepoTags: []string{"docker.io/library/nginx:sentinel-backup-20260101-000000"}},
	})
	if slices.Contains(mock.removeImageCalls, "sha256:old111") {
		t.Errorf("removed %v, want the backed-up image kept", mock.removeImageCalls)
	}
}

// Only the newest backups of a repository are kept, so they do not pile up.
func TestImageBackupsArePruned(t *testing.T) {
	mock, _ := backupUpdate(t, []docker.ImageSummary{
		{ID: "sha256:a", RepoTags: []string{"docker.io/library/nginx:sentinel-backup-20250101-000000"}},
		{ID: "sha256:b", RepoTags: []string{"docker.io/library/nginx:sentinel-backup-20250201-000000"}},
		{ID: "sha256:c", RepoTags: []string{"docker.io/library/nginx:sentinel-backup-20250301-000000", "docker.io/library/nginx:1.0"}},
		{ID: "sha256:old111", RepoTags: []string{"docker.io/library/nginx:sentinel-backup-20260101-000000"}},
		{ID: "sha256:z", RepoTags: []string{"docker.io/library/redis:sentinel-backup-20240101-000000"}},
	})
	want := []string{"docker.io/library/nginx:sentinel-backup-20250101-000000"}
	var removed []string
	for _, r := range mock.removeImageCalls {
		if r != "sha256:old111" {
			removed = append(removed, r)
		}
	}
	if !slices.Equal(removed, want) {
		t.Errorf("removed %v, want only the oldest nginx backup beyond the newest three", removed)
	}
}

// The daemon lists Docker Hub tags in short form (nginx:...), while the
// container may name the image in full (docker.io/library/nginx:latest):
// backups are matched by repository, not by text.
func TestImageBackupsArePrunedForShortHubTags(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	u.docker = &imagesDocker{mockDocker: mock, images: []docker.ImageSummary{
		{ID: "sha256:a", RepoTags: []string{"nginx:sentinel-backup-20250101-000000"}},
		{ID: "sha256:b", RepoTags: []string{"nginx:sentinel-backup-20250201-000000"}},
		{ID: "sha256:c", RepoTags: []string{"nginx:sentinel-backup-20250301-000000"}},
		{ID: "sha256:old111", RepoTags: []string{"nginx:sentinel-backup-20260101-000000"}},
		{ID: "sha256:z", RepoTags: []string{"other/nginx:sentinel-backup-20240101-000000"}},
	}}
	u.cfg.SetImageBackup(true)
	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !slices.Equal(mock.removeImageCalls, []string{"nginx:sentinel-backup-20250101-000000"}) {
		t.Errorf("removed %v, want only the oldest nginx backup", mock.removeImageCalls)
	}
}
