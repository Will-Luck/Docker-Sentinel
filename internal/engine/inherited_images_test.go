package engine

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// setupInheritedUpdate builds the smoke test's lh-mut case: the container
// was created on app 1.0.0 and updated to 1.0.1 by an older Sentinel, which
// copied its whole config, so it still carries 1.0.0's env, label and
// command. The history has the row that older Sentinel wrote.
func setupInheritedUpdate(t *testing.T) (*fakeDocker, *Updater) {
	t.Helper()
	f := newFakeDocker()
	f.imageIDs["app:edge"] = "sha256:v200"
	f.imageConfigs["sha256:v100"] = &container.Config{
		Env:    []string{"PATH=/usr/bin", "APP_VERSION=1.0.0", "IMG_ONLY_V1_0_0=1"},
		Labels: map[string]string{"org.opencontainers.image.version": "1.0.0"},
		Cmd:    []string{"start-v1.0.0"},
	}
	f.imageConfigs["sha256:v101"] = &container.Config{
		Env:    []string{"PATH=/usr/bin", "APP_VERSION=1.0.1", "IMG_ONLY_V1_0_1=1"},
		Labels: map[string]string{"org.opencontainers.image.version": "1.0.1"},
		Cmd:    []string{"start-v1.0.1"},
	}
	old := f.add("old", "app", "app:edge", true, &container.Config{
		Env:    []string{"PATH=/usr/bin", "APP_VERSION=1.0.0", "IMG_ONLY_V1_0_0=1", "IMG_ONLY_V1_0_1=1", "TOKEN=x"},
		Labels: map[string]string{"org.opencontainers.image.version": "1.0.0", "mine": "1"},
		Cmd:    []string{"start-v1.0.0"},
	}, nil)
	old.imageID = "sha256:v101"
	u := newFakeUpdater(t, f)
	if err := u.store.RecordUpdate(store.UpdateRecord{
		ContainerName: "app", OldImage: "app:edge", OldDigest: "sha256:v100",
		NewImage: "app:edge", NewDigest: "app@sha256:d101", Outcome: "success",
	}); err != nil {
		t.Fatal(err)
	}
	return f, u
}

// An update strips the values the container inherited from any image it
// ran, found through the history, not only from the image it runs now.
func TestUpdateStripsDefaultsOfImagesFromHistory(t *testing.T) {
	f, u := setupInheritedUpdate(t)

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err != nil {
		t.Fatalf("update: %v", err)
	}

	c := f.byName("app")
	if c == nil {
		t.Fatalf("containers = %v", f.names())
	}
	if !slices.Equal(c.cfg.Env, []string{"TOKEN=x"}) {
		t.Errorf("Env = %v, want only the user's TOKEN (1.0.0's values were inherited)", c.cfg.Env)
	}
	if c.cfg.Labels["org.opencontainers.image.version"] != "" || c.cfg.Labels["mine"] != "1" {
		t.Errorf("Labels = %v, want the old version label dropped and the user's kept", c.cfg.Labels)
	}
	if c.cfg.Cmd != nil {
		t.Errorf("Cmd = %v, want 1.0.0's command dropped", c.cfg.Cmd)
	}
}

// History rows of another repository (a different app that once had the
// same container name) are not used.
func TestUpdateIgnoresHistoryOfAnotherRepository(t *testing.T) {
	f, u := setupInheritedUpdate(t)
	if err := u.store.RecordUpdate(store.UpdateRecord{
		ContainerName: "app", OldImage: "other/db:1", OldDigest: "sha256:db1",
		NewImage: "other/db:2", Outcome: "success",
	}); err != nil {
		t.Fatal(err)
	}
	f.imageConfigs["sha256:db1"] = &container.Config{Env: []string{"TOKEN=x"}}

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	if c := f.byName("app"); c == nil || !slices.Contains(c.cfg.Env, "TOKEN=x") {
		t.Errorf("containers = %v, want the user's TOKEN kept", f.names())
	}
}

// Only rows of updates that changed the image count towards the last 10:
// failed attempts and refusals since then do not push the image the
// container ran first out of the window.
func TestInheritedImagesSkipRowsThatChangedNothing(t *testing.T) {
	f, u := setupInheritedUpdate(t)
	for i := range 12 {
		if err := u.store.RecordUpdate(store.UpdateRecord{
			Timestamp: time.Unix(int64(1000+i), 0), ContainerName: "app",
			OldImage: "app:edge", OldDigest: "sha256:v101", Outcome: "failed", Error: "pull failed",
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	if c := f.byName("app"); c == nil || !slices.Equal(c.cfg.Env, []string{"TOKEN=x"}) {
		t.Errorf("containers = %v env = %v, want 1.0.0's values stripped despite the failed attempts", f.names(), c.cfg.Env)
	}
}
