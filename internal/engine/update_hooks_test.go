package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
)

// hookStoreStub implements hooks.Store in memory.
type hookStoreStub struct {
	hooks map[string][]hooks.Hook
}

func (s *hookStoreStub) ListHooks(name string) ([]hooks.Hook, error) { return s.hooks[name], nil }
func (s *hookStoreStub) SaveHook(h hooks.Hook) error {
	s.hooks[h.ContainerName] = append(s.hooks[h.ContainerName], h)
	return nil
}
func (s *hookStoreStub) DeleteHook(string, string) error { return nil }

// setupHookUpdate builds the standard "aaa" -> "new-nginx" update with hooks
// enabled, the given labels on the old container and a real hooks.Runner over
// the mock Docker client and an in-memory hook store.
func setupHookUpdate(t *testing.T, labels map[string]string) (*mockDocker, *Updater, *hookStoreStub) {
	t.Helper()
	mock, u := setupUpdateWithScanVerify(t)

	old := mock.inspectResults["aaa"]
	old.Config.Labels = labels
	mock.inspectResults["aaa"] = old

	stub := &hookStoreStub{hooks: make(map[string][]hooks.Hook)}
	u.SetHookRunner(hooks.NewRunner(mock, stub, slog.New(slog.NewTextHandler(io.Discard, nil))))
	u.cfg.SetHooksEnabled(true)
	return mock, u, stub
}

func TestUpdateContainer_LabelHooksRun(t *testing.T) {
	mock, u, _ := setupHookUpdate(t, map[string]string{
		"sentinel.hook.pre-update":  "wget -qO- http://api.example/pre",
		"sentinel.hook.post-update": "wget -qO- http://api.example/post",
	})

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}

	want := []string{"aaa", "new-nginx"}
	if len(mock.execCalls) != len(want) {
		t.Fatalf("execCalls = %v, want %v (pre on old, post on new)", mock.execCalls, want)
	}
	for i, id := range want {
		if mock.execCalls[i] != id {
			t.Errorf("execCalls[%d] = %q, want %q", i, mock.execCalls[i], id)
		}
	}
}

func TestUpdateContainer_LabelHooksSkippedWhenDisabled(t *testing.T) {
	mock, u, _ := setupHookUpdate(t, map[string]string{
		"sentinel.hook.pre-update": "echo pre",
	})
	u.cfg.SetHooksEnabled(false)

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	if len(mock.execCalls) != 0 {
		t.Errorf("execCalls = %v, want none while hooks are disabled", mock.execCalls)
	}
}

// assertAbortedBeforePull checks that a failed pre-update hook stopped the
// update before any backup, pull or destructive step, and cleared maintenance.
func assertAbortedBeforePull(t *testing.T, mock *mockDocker, u *Updater) {
	t.Helper()
	if len(mock.tagImageCalls) != 0 {
		t.Errorf("tagImageCalls = %v, want none (abort before backup)", mock.tagImageCalls)
	}
	if len(mock.pullCalls) != 0 {
		t.Errorf("pullCalls = %v, want none (abort before pull)", mock.pullCalls)
	}
	if len(mock.stopCalls) != 0 || len(mock.removeCalls) != 0 || len(mock.createCalls) != 0 {
		t.Errorf("stop/remove/create = %v/%v/%v, want none", mock.stopCalls, mock.removeCalls, mock.createCalls)
	}
	if on, err := u.store.GetMaintenance("nginx"); err != nil || on {
		t.Errorf("maintenance = %v (err %v), want cleared", on, err)
	}
}

func TestUpdateContainer_PreUpdateNonZeroExitAborts(t *testing.T) {
	mock, u, _ := setupHookUpdate(t, map[string]string{"sentinel.hook.pre-update": "pg_dump"})
	u.cfg.SetImageBackup(true)
	mock.execResults["aaa"] = struct {
		exitCode int
		output   string
	}{exitCode: 1, output: "pg_dump: connection refused"}

	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")
	if err == nil {
		t.Fatal("expected an error when the pre-update hook exits 1")
	}
	assertAbortedBeforePull(t, mock, u)
}

func TestUpdateContainer_PreUpdateExecErrorAborts(t *testing.T) {
	mock, u, stub := setupHookUpdate(t, nil)
	stub.hooks["nginx"] = []hooks.Hook{
		{ContainerName: "nginx", Phase: "pre-update", Command: []string{"/backup.sh"}, Timeout: 10},
	}
	mock.execErr["aaa"] = errors.New("exec create: no such file")

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err == nil {
		t.Fatal("expected an error when the pre-update hook cannot be executed")
	}
	assertAbortedBeforePull(t, mock, u)
}

func TestUpdateContainer_PreUpdateTimeoutAborts(t *testing.T) {
	mock, u, _ := setupHookUpdate(t, map[string]string{
		"sentinel.hook.pre-update": "sleep 600",
		"sentinel.hook.timeout":    "1",
	})
	mock.execBlock["aaa"] = true

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err == nil {
		t.Fatal("expected an error when the pre-update hook times out")
	}
	assertAbortedBeforePull(t, mock, u)
}

func TestUpdateContainer_PreUpdateExit75Skips(t *testing.T) {
	mock, u, _ := setupHookUpdate(t, map[string]string{"sentinel.hook.pre-update": "exit 75"})
	mock.execResults["aaa"] = struct {
		exitCode int
		output   string
	}{exitCode: 75}

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("exit 75 should skip without an error, got %v", err)
	}
	assertAbortedBeforePull(t, mock, u)
}

func TestUpdateContainer_PostUpdateFailureOnlyWarns(t *testing.T) {
	mock, u, _ := setupHookUpdate(t, map[string]string{"sentinel.hook.post-update": "curl -sf http://localhost/health"})
	mock.execResults["new-nginx"] = struct {
		exitCode int
		output   string
	}{exitCode: 1}

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("a failed post-update hook must not fail the update, got %v", err)
	}
	if len(mock.execCalls) != 1 || mock.execCalls[0] != "new-nginx" {
		t.Errorf("execCalls = %v, want one post-update exec on new-nginx", mock.execCalls)
	}
}
