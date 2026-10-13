package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
	"github.com/moby/moby/api/types/swarm"
)

func setupServiceVerify(t *testing.T, labels map[string]string) (*mockDocker, *Updater) {
	t.Helper()
	mock := newMockDocker()
	mock.inspectService["svc-1"] = swarm.Service{
		ID:           "svc-1",
		Meta:         swarm.Meta{Version: swarm.Version{Index: 10}},
		Spec:         svcSpec("web", "nginx:1.25", labels),
		UpdateStatus: &swarm.UpdateStatus{State: swarm.UpdateStateCompleted},
	}
	u, _ := newSwarmTestUpdater(t, mock)
	return mock, u
}

// A Swarm service update goes through the same signature gate as a
// container update: an enforced check that fails blocks it before the
// service is changed.
func TestServiceUpdateVerifyEnforceBlocks(t *testing.T) {
	mock, u := setupServiceVerify(t, nil)
	mv := &mockImageVerifier{result: &verify.Result{Verified: false, Error: "no matching signatures"}}
	u.SetVerifier(mv)
	u.SetVerifyMode(verify.ModeEnforce)

	err := u.UpdateService(context.Background(), "svc-1", "web", "nginx:1.26")

	if err == nil || len(mock.updateSvcCalls) != 0 {
		t.Fatalf("err = %v, service updates = %d, want the unsigned image blocked", err, len(mock.updateSvcCalls))
	}
	if len(mv.calls) != 1 || mv.calls[0] != "nginx:1.26" {
		t.Errorf("verifier calls = %v, want the target image checked", mv.calls)
	}
}

// Without cosign, enforce refuses the service update and records why.
func TestServiceUpdateVerifyEnforceWithoutCosignRefuses(t *testing.T) {
	mock, u := setupServiceVerify(t, nil)
	u.SetVerifyMode(verify.ModeEnforce)

	err := u.UpdateService(context.Background(), "svc-1", "web", "nginx:1.26")

	if err == nil || !strings.Contains(err.Error(), verifierMissing) || len(mock.updateSvcCalls) != 0 {
		t.Fatalf("err = %v, service updates = %d, want the update refused", err, len(mock.updateSvcCalls))
	}
	history, _ := u.store.ListHistoryByContainer("web", 10)
	if len(history) != 1 || history[0].Outcome != "failed" || history[0].Type != "service" {
		t.Errorf("history = %+v, want one failed service row", history)
	}
}

// Warn mode and a sentinel.verify=disabled label let the update through.
func TestServiceUpdateVerifyWarnAndLabel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   verify.Mode
		labels map[string]string
		calls  int
	}{
		{"warn mode", verify.ModeWarn, nil, 1},
		{"label disables", verify.ModeEnforce, map[string]string{verify.ContainerLabel: "disabled"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock, u := setupServiceVerify(t, tc.labels)
			mv := &mockImageVerifier{result: &verify.Result{Verified: false, Error: "no matching signatures"}}
			u.SetVerifier(mv)
			u.SetVerifyMode(tc.mode)

			_ = u.UpdateService(context.Background(), "svc-1", "web", "nginx:1.26")

			if len(mock.updateSvcCalls) != 1 || len(mv.calls) != tc.calls {
				t.Errorf("service updates = %d, verifier calls = %d, want 1 and %d", len(mock.updateSvcCalls), len(mv.calls), tc.calls)
			}
		})
	}
}
