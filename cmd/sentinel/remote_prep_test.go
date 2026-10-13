package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
)

type fakeVerifier struct {
	result *verify.Result
	calls  []string
}

func (f *fakeVerifier) Verify(_ context.Context, ref string) *verify.Result {
	f.calls = append(f.calls, ref)
	return f.result
}

// newTestPrep returns a prep over a fresh database, knowing one remote
// container, web on nginx:1 on host h1.
func newTestPrep(t *testing.T, labels map[string]string) (*remoteUpdatePrep, *engine.Updater) {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/sentinel.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	log := logging.New(false)
	u := engine.NewUpdater(nil, nil, db, nil, config.NewTestConfig(), log, clock.Real{}, notify.NewMulti(log), nil)
	web := cluster.ContainerInfo{Name: "web", Image: "nginx:1", Labels: labels}
	return &remoteUpdatePrep{db: db, updater: u, container: func(hostID, name string) (cluster.ContainerInfo, bool) {
		if hostID == "h1" && name == "web" {
			return web, true
		}
		return cluster.ContainerInfo{}, false
	}}, u
}

// The signature gate applies to every remote update before it is sent;
// a digest-only update (no target) checks the container's own image, and a
// rollback is not checked.
func TestRemoteUpdatePrep_SignatureGate(t *testing.T) {
	p, u := newTestPrep(t, map[string]string{verify.ContainerLabel: "enforce"})
	v := &fakeVerifier{result: &verify.Result{Verified: false, Error: "no matching signatures"}}
	u.SetVerifier(v)

	err := p.prepare(context.Background(), "h1", &proto.UpdateContainerRequest{ContainerName: "web", TargetImage: "nginx:2"}, false)
	if err == nil {
		t.Fatal("an unsigned image passed the enforce gate")
	}
	err = p.prepare(context.Background(), "h1", &proto.UpdateContainerRequest{ContainerName: "web"}, false)
	if err == nil {
		t.Fatal("an unsigned digest-only update passed the enforce gate")
	}
	if len(v.calls) != 2 || v.calls[0] != "nginx:2" || v.calls[1] != "nginx:1" {
		t.Errorf("verified %v, want [nginx:2 nginx:1]", v.calls)
	}
	if err := p.prepare(context.Background(), "h1", &proto.UpdateContainerRequest{ContainerName: "web", TargetImage: "nginx:0"}, true); err != nil {
		t.Errorf("rollback refused: %v", err)
	}

	v.result = &verify.Result{Verified: true}
	if err := p.prepare(context.Background(), "h1", &proto.UpdateContainerRequest{ContainerName: "web", TargetImage: "nginx:2"}, false); err != nil {
		t.Errorf("a signed image was refused: %v", err)
	}
}

// Enforce without cosign refuses the remote update, as it does a local one.
func TestRemoteUpdatePrep_EnforceWithoutCosign(t *testing.T) {
	p, u := newTestPrep(t, nil)
	u.SetVerifyMode(verify.ModeEnforce)
	err := p.prepare(context.Background(), "h1", &proto.UpdateContainerRequest{ContainerName: "web", TargetImage: "nginx:2"}, false)
	if !errors.Is(err, engine.ErrVerifierUnavailable) {
		t.Fatalf("err = %v, want ErrVerifierUnavailable", err)
	}
}

// The hub sends the images a remote container ran before, from its update
// history: newest first, each once, not the current one, at most 10, and
// only those whose digest it knows.
func TestRemoteUpdatePrep_SendsPreviousImages(t *testing.T) {
	p, _ := newTestPrep(t, nil)
	key := store.ScopedKey("h1", "web")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	add := func(i int, rec store.UpdateRecord) {
		t.Helper()
		rec.Timestamp = base.Add(time.Duration(i) * time.Minute)
		rec.ContainerName = key
		rec.HostID = "h1"
		if err := p.db.RecordUpdate(rec); err != nil {
			t.Fatal(err)
		}
	}
	add(0, store.UpdateRecord{OldImage: "nginx:0.9", OldDigest: "nginx@sha256:a", Outcome: "success"})
	add(1, store.UpdateRecord{OldImage: "nginx:0.9", OldDigest: "nginx@sha256:a", Outcome: "failed"})
	add(2, store.UpdateRecord{OldImage: "nginx:0.10", Outcome: "success"}) // no digest
	add(3, store.UpdateRecord{Outcome: "rollback"})
	add(4, store.UpdateRecord{OldImage: "nginx:0.11", OldDigest: "sha256:b", Outcome: "success"})
	add(5, store.UpdateRecord{OldImage: "nginx:1", OldDigest: "nginx@sha256:current", Outcome: "failed"})
	for i := range 12 {
		if err := p.db.RecordUpdate(store.UpdateRecord{Timestamp: base.Add(time.Hour), ContainerName: store.ScopedKey("h2", "web"),
			OldImage: "other", OldDigest: fmt.Sprintf("sha256:other-%d", i), Outcome: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	p.container = func(hostID, name string) (cluster.ContainerInfo, bool) {
		return cluster.ContainerInfo{Name: "web", Image: "nginx:1", ImageDigest: "nginx@sha256:current"}, true
	}

	req := &proto.UpdateContainerRequest{ContainerName: "web", TargetImage: "nginx:2"}
	if err := p.prepare(context.Background(), "h1", req, false); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, pi := range req.GetPreviousImages() {
		got = append(got, pi.GetRef()+" "+pi.GetDigest())
	}
	want := []string{"nginx:0.11 sha256:b", "nginx:0.9 nginx@sha256:a"}
	if !slices.Equal(got, want) {
		t.Errorf("previous images = %q, want %q", got, want)
	}
}

// The hooks the hub stores for a remote container (its host-scoped key)
// go to the agent with the update; a rollback runs none, as locally.
func TestRemoteUpdatePrep_SendsTheStoredHooks(t *testing.T) {
	p, _ := newTestPrep(t, nil)
	for _, h := range []store.HookEntry{
		{ContainerName: store.ScopedKey("h1", "web"), Phase: "pre-update", Command: []string{"/bin/sh", "-c", "pg_dump"}, Timeout: 60},
		{ContainerName: store.ScopedKey("h1", "web"), Phase: "post-update", Command: []string{"notify"}, Timeout: 30},
		{ContainerName: "web", Phase: "pre-update", Command: []string{"local-only"}},
		{ContainerName: store.ScopedKey("h2", "web"), Phase: "pre-update", Command: []string{"other-host"}},
	} {
		if err := p.db.SaveHook(h); err != nil {
			t.Fatal(err)
		}
	}

	req := &proto.UpdateContainerRequest{ContainerName: "web", TargetImage: "nginx:2"}
	if err := p.prepare(context.Background(), "h1", req, false); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range req.GetHooks() {
		got = append(got, fmt.Sprintf("%s %v %d", h.GetPhase(), h.GetCommand(), h.GetTimeoutSeconds()))
	}
	want := []string{"post-update [notify] 30", "pre-update [/bin/sh -c pg_dump] 60"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("hooks sent = %q, want %q", got, want)
	}

	rb := &proto.UpdateContainerRequest{ContainerName: "web", TargetImage: "nginx:1"}
	if err := p.prepare(context.Background(), "h1", rb, true); err != nil {
		t.Fatal(err)
	}
	if !rb.GetSkipHooks() || len(rb.GetHooks()) != 0 {
		t.Errorf("rollback request: skip_hooks %v, hooks %v; want hooks skipped", rb.GetSkipHooks(), rb.GetHooks())
	}
}

// An agent's answer becomes the caller's error: none for success, a
// recognisable one when a pre-update hook asked to skip.
func TestRemoteResultErr(t *testing.T) {
	if err := remoteResultErr(&proto.UpdateResult{Outcome: "success", Error: "a warning"}, "update"); err != nil {
		t.Errorf("success: %v", err)
	}
	if err := remoteResultErr(&proto.UpdateResult{Outcome: "skipped", Error: "a pre-update hook asked to skip the update (exit 75)"}, "update"); !errors.Is(err, cluster.ErrUpdateSkipped) {
		t.Errorf("skipped: %v, want ErrUpdateSkipped", err)
	}
	if err := remoteResultErr(&proto.UpdateResult{Outcome: "failed", Error: "pull failed"}, "update"); err == nil || err.Error() != "pull failed" {
		t.Errorf("failed: %v, want the agent's error", err)
	}
	if err := remoteResultErr(&proto.UpdateResult{Outcome: "failed"}, "rollback"); err == nil || err.Error() != "rollback failed" {
		t.Errorf("failed without detail: %v", err)
	}
}

// The hub sends the settings its own updates use.
func TestSettingsSyncFrom(t *testing.T) {
	ss := settingsSyncFrom(engine.AgentSettings{
		PollInterval: time.Hour, GracePeriod: time.Minute, LatestAutoUpdate: true, ImageCleanup: true,
		ImageBackup: true, RemoveVolumes: true, HooksEnabled: true, DependencyAware: true, RollbackPolicy: "manual",
	})
	if ss.GetPollInterval().AsDuration() != time.Hour || ss.GetGracePeriod().AsDuration() != time.Minute ||
		!ss.GetLatestAutoUpdate() || !ss.GetImageCleanup() || !ss.GetImageBackup() || !ss.GetRemoveVolumes() ||
		!ss.GetHooksEnabled() || !ss.GetDependencyAware() || ss.GetRollbackPolicy() != "manual" {
		t.Errorf("SettingsSync = %v, want every setting carried", ss)
	}
}

// The settings an agent's own environment sets win over the hub's.
func TestAgentEnvSettings(t *testing.T) {
	env := map[string]string{"SENTINEL_IMAGE_CLEANUP": "false", "SENTINEL_HOOKS": "true", "SENTINEL_POLL_INTERVAL": "1h"}
	got := agentEnvSettings(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if !got["image_cleanup"] || !got["hooks_enabled"] || got["remove_volumes"] || got["image_backup"] || got["dependency_aware"] {
		t.Errorf("env settings = %v, want image_cleanup and hooks_enabled only", got)
	}
}
