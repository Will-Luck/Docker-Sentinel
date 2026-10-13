package engine

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
	"github.com/moby/moby/api/types/container"
)

const verifierMissing = "signature verification is set to enforce but cosign is not available"

// With verification set to enforce and no cosign (the official image ships
// none, so no verifier is attached), an update must not go ahead
// unverified: it is refused before anything changes and recorded as a
// failed update saying why.
func TestVerifyEnforceWithoutCosignRefusesUpdate(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	u.SetVerifyMode(verify.ModeEnforce) // no SetVerifier: cosign was not found

	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")

	if err == nil || !strings.Contains(err.Error(), verifierMissing) {
		t.Fatalf("err = %v, want the update refused because cosign is not available", err)
	}
	if len(mock.pullCalls) != 0 || len(mock.stopCalls) != 0 || len(mock.createCalls) != 0 {
		t.Errorf("pulls %d, stops %d, creates %d, want nothing done", len(mock.pullCalls), len(mock.stopCalls), len(mock.createCalls))
	}
	history, _ := u.store.ListHistoryByContainer("nginx", 10)
	if len(history) != 1 || history[0].Outcome != "failed" || history[0].Error != verifierMissing {
		t.Errorf("history = %+v, want one failed row giving the reason", history)
	}
}

// Warn mode without cosign still updates.
func TestVerifyWarnWithoutCosignProceeds(t *testing.T) {
	_, u := setupUpdateWithScanVerify(t)
	u.SetVerifyMode(verify.ModeWarn)

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("err = %v, want warn mode to update", err)
	}
}

// The mode is saved from the dashboard without a restart, so the engine
// reads the current setting rather than the value it was started with.
func TestVerifyModeChangedAtRuntimeApplies(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	mv := &mockImageVerifier{result: &verify.Result{Verified: false, Error: "no matching signatures"}}
	u.SetVerifier(mv)
	u.SetVerifyMode(verify.ModeDisabled) // as read at startup
	u.SetSettingsReader(u.store)
	if err := u.store.SaveSetting(store.SettingVerifyMode, "enforce"); err != nil {
		t.Fatal(err)
	}

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err == nil {
		t.Fatal("expected the unsigned image to be blocked by the enforce mode saved at runtime")
	}
	if len(mv.calls) != 1 || len(mock.stopCalls) != 0 {
		t.Errorf("verifier calls %d, stops %d, want the image checked and the update blocked", len(mv.calls), len(mock.stopCalls))
	}
}

// A sentinel.verify=enforce label applies even when the global mode is
// disabled, as ResolveMode documents (label first).
func TestVerifyLabelEnforcesWhenGlobalDisabled(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	old := mock.inspectResults["aaa"]
	old.Config.Labels = map[string]string{verify.ContainerLabel: "enforce"}
	mock.inspectResults["aaa"] = old
	mv := &mockImageVerifier{result: &verify.Result{Verified: false, Error: "no matching signatures"}}
	u.SetVerifier(mv)
	u.SetVerifyMode(verify.ModeDisabled) // as main.go sets it from an empty setting

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err == nil {
		t.Fatal("expected the label to enforce verification")
	}
	if len(mv.calls) != 1 || len(mock.stopCalls) != 0 {
		t.Errorf("verifier calls %d, stops %d, want the image checked and the update blocked", len(mv.calls), len(mock.stopCalls))
	}
}

// A scan that refuses several updates for want of cosign notifies once,
// naming them all, instead of once per container.
func TestScanNotifiesOnceForUpdatesRefusedWithoutCosign(t *testing.T) {
	mock := newMockDocker()
	auto := map[string]string{"sentinel.policy": "auto"}
	for _, c := range []struct{ id, name string }{{"aaa", "nginx"}, {"bbb", "redis"}} {
		ref := "localhost:1/" + c.name + ":latest"
		mock.containers = append(mock.containers, container.Summary{ID: c.id, Names: []string{"/" + c.name}, Image: ref, Labels: auto})
		mock.imageDigests[ref] = "localhost:1/" + c.name + "@sha256:old"
		mock.distDigests[ref] = "sha256:new"
		mock.inspectResults[c.id] = container.InspectResponse{
			ID: c.id, Name: "/" + c.name,
			Config:          &container.Config{Image: ref, Labels: auto},
			HostConfig:      &container.HostConfig{},
			NetworkSettings: &container.NetworkSettings{},
		}
	}
	u, _ := newTestUpdater(t, mock)
	u.SetVerifyMode(verify.ModeEnforce)
	rec := recordNotifications(u)

	result := u.Scan(context.Background(), ScanScheduled)

	if result.Failed != 2 || len(mock.pullCalls) != 0 {
		t.Errorf("failed = %d, pulls = %d, want both refused before any pull", result.Failed, len(mock.pullCalls))
	}
	if n := len(rec.ofType(notify.EventUpdateStarted)); n != 0 {
		t.Errorf("update-started notifications = %d, want none", n)
	}
	failed := rec.ofType(notify.EventUpdateFailed)
	if len(failed) != 1 || !slices.Equal(failed[0].ContainerNames, []string{"nginx", "redis"}) || !strings.Contains(failed[0].Error, verifierMissing) {
		t.Errorf("failure notifications = %+v, want one naming nginx and redis", failed)
	}
}

// repoDigestDocker reports the repository digests of pulled images.
type repoDigestDocker struct {
	*mockDocker
	digests map[string][]string
}

func (d *repoDigestDocker) ImageRepoDigests(_ context.Context, ref string) ([]string, error) {
	return d.digests[ref], nil
}

// The signature is checked on the digest the update pulled (in the image's
// own repository), not on the tag, which the registry could move between
// the check and the create.
func TestVerifyChecksThePulledDigest(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	u.docker = &repoDigestDocker{mockDocker: mock, digests: map[string][]string{
		"docker.io/library/nginx:latest": {"docker.io/library/other@sha256:aaa", "docker.io/library/nginx@sha256:bbb"},
	}}
	mv := &mockImageVerifier{result: &verify.Result{Verified: true}}
	u.SetVerifier(mv)
	u.SetVerifyMode(verify.ModeEnforce)

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(mv.calls) != 1 || mv.calls[0] != "docker.io/library/nginx@sha256:bbb" {
		t.Errorf("verified %v, want the pulled digest in nginx's repository", mv.calls)
	}
}

// Keyless verification without an identity refuses every enforced update;
// like a missing cosign, each refusal is a failed history row, and a scan
// sends one notification naming the settings to set.
func TestScanRefusesKeylessWithoutIdentityVisibly(t *testing.T) {
	mock := newMockDocker()
	auto := map[string]string{"sentinel.policy": "auto"}
	for _, c := range []struct{ id, name string }{{"aaa", "nginx"}, {"bbb", "redis"}} {
		ref := "localhost:1/" + c.name + ":latest"
		mock.containers = append(mock.containers, container.Summary{ID: c.id, Names: []string{"/" + c.name}, Image: ref, Labels: auto})
		mock.imageDigests[ref] = "localhost:1/" + c.name + "@sha256:old"
		mock.distDigests[ref] = "sha256:new"
		mock.inspectResults[c.id] = container.InspectResponse{
			ID: c.id, Name: "/" + c.name,
			Config:          &container.Config{Image: ref, Labels: auto},
			HostConfig:      &container.HostConfig{},
			NetworkSettings: &container.NetworkSettings{},
		}
	}
	u, clk := newTestUpdater(t, mock)
	u.clock = tickClock{clk} // history rows are keyed by time
	u.SetVerifier(verify.New(nil, verify.WithCosignPath("/nonexistent/cosign"), verify.WithKeyless()))
	u.SetVerifyMode(verify.ModeEnforce)
	rec := recordNotifications(u)

	result := u.Scan(context.Background(), ScanScheduled)

	if result.Failed != 2 || len(mock.pullCalls) != 0 {
		t.Errorf("failed = %d, pulls = %d, want both refused before any pull", result.Failed, len(mock.pullCalls))
	}
	failed := rec.ofType(notify.EventUpdateFailed)
	if len(failed) != 1 || !slices.Equal(failed[0].ContainerNames, []string{"nginx", "redis"}) || !strings.Contains(failed[0].Error, "cosign_identity") {
		t.Errorf("failure notifications = %+v, want one naming both and the settings to set", failed)
	}
	for _, name := range []string{"nginx", "redis"} {
		history, _ := u.store.ListHistoryByContainer(name, 10)
		if len(history) != 1 || history[0].Outcome != "failed" || !strings.Contains(history[0].Error, "cosign_oidc_issuer") {
			t.Errorf("%s history = %+v, want a failed row naming the settings", name, history)
		}
	}
}

// tickClock moves on a second each time it is read.
type tickClock struct{ *mockClock }

func (c tickClock) Now() time.Time {
	c.Advance(time.Second)
	return c.mockClock.Now()
}
