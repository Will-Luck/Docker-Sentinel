package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
)

// eventRecorder is a notify.Notifier that keeps every event it is sent.
type eventRecorder struct {
	mu     sync.Mutex
	events []notify.Event
}

func (r *eventRecorder) Send(_ context.Context, e notify.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *eventRecorder) Name() string { return "recorder" }

func (r *eventRecorder) ofType(t notify.EventType) []notify.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []notify.Event
	for _, e := range r.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// recordNotifications routes the updater's notifications to a recorder.
func recordNotifications(u *Updater) *eventRecorder {
	rec := &eventRecorder{}
	u.notifier.Reconfigure(rec)
	return rec
}

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

// assertAbortReported checks that an aborted pre-update left one failed
// history record and sent exactly one update-failed notification, both
// carrying wantErr.
func assertAbortReported(t *testing.T, u *Updater, rec *eventRecorder, wantErr string) {
	t.Helper()
	history, err := u.store.ListHistory(10, "")
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history = %+v, want one record", history)
	}
	h := history[0]
	if h.ContainerName != "nginx" || h.Outcome != "failed" {
		t.Errorf("history record = %s/%s, want nginx/failed", h.ContainerName, h.Outcome)
	}
	if h.OldImage != "docker.io/library/nginx:latest" {
		t.Errorf("history old image = %q, want the running image", h.OldImage)
	}
	if !strings.Contains(h.Error, wantErr) {
		t.Errorf("history error = %q, want it to contain %q", h.Error, wantErr)
	}

	failed := rec.ofType(notify.EventUpdateFailed)
	if len(failed) != 1 {
		t.Fatalf("update_failed notifications = %d, want exactly 1", len(failed))
	}
	if failed[0].ContainerName != "nginx" || failed[0].OldImage != "docker.io/library/nginx:latest" {
		t.Errorf("update_failed event = %s/%s, want nginx and the running image", failed[0].ContainerName, failed[0].OldImage)
	}
	if !strings.Contains(failed[0].Error, wantErr) {
		t.Errorf("update_failed error = %q, want it to contain %q", failed[0].Error, wantErr)
	}
}

func TestUpdateContainer_PreUpdateNonZeroExitAborts(t *testing.T) {
	mock, u, _ := setupHookUpdate(t, map[string]string{"sentinel.hook.pre-update": "pg_dump"})
	rec := recordNotifications(u)
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
	assertAbortReported(t, u, rec, "pre-update hook exited with code 1")
}

func TestUpdateContainer_PreUpdateExecErrorAborts(t *testing.T) {
	mock, u, stub := setupHookUpdate(t, nil)
	rec := recordNotifications(u)
	stub.hooks["nginx"] = []hooks.Hook{
		{ContainerName: "nginx", Phase: "pre-update", Command: []string{"/backup.sh"}, Timeout: 10},
	}
	mock.execErr["aaa"] = errors.New("exec create: no such file")

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err == nil {
		t.Fatal("expected an error when the pre-update hook cannot be executed")
	}
	assertAbortedBeforePull(t, mock, u)
	assertAbortReported(t, u, rec, "exec create: no such file")
}

func TestUpdateContainer_PreUpdateTimeoutAborts(t *testing.T) {
	mock, u, _ := setupHookUpdate(t, map[string]string{
		"sentinel.hook.pre-update": "sleep 600",
		"sentinel.hook.timeout":    "1",
	})
	rec := recordNotifications(u)
	mock.execBlock["aaa"] = true

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err == nil {
		t.Fatal("expected an error when the pre-update hook times out")
	}
	assertAbortedBeforePull(t, mock, u)
	assertAbortReported(t, u, rec, "timed out after 1s")
}

func TestUpdateContainer_PreUpdateCancelledIsNotRecorded(t *testing.T) {
	mock, u, _ := setupHookUpdate(t, map[string]string{"sentinel.hook.pre-update": "sleep 600"})
	rec := recordNotifications(u)
	mock.execBlock["aaa"] = true

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	err := u.UpdateContainer(ctx, "aaa", "nginx", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertAbortedBeforePull(t, mock, u)
	if history, _ := u.store.ListHistory(10, ""); len(history) != 0 {
		t.Errorf("history = %+v, want none for a cancelled update", history)
	}
	if failed := rec.ofType(notify.EventUpdateFailed); len(failed) != 0 {
		t.Errorf("update_failed notifications = %d, want none for a cancelled update", len(failed))
	}
}

func TestUpdateContainer_PreUpdateExit75Skips(t *testing.T) {
	mock, u, _ := setupHookUpdate(t, map[string]string{"sentinel.hook.pre-update": "exit 75"})
	rec := recordNotifications(u)
	mock.execResults["aaa"] = struct {
		exitCode int
		output   string
	}{exitCode: 75}

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("exit 75 should skip without an error, got %v", err)
	}
	assertAbortedBeforePull(t, mock, u)

	history, err := u.store.ListHistory(10, "")
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("history = %+v, want none for a requested skip", history)
	}
	if failed := rec.ofType(notify.EventUpdateFailed); len(failed) != 0 {
		t.Errorf("update_failed notifications = %d, want none for a requested skip", len(failed))
	}
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

func TestUpdateContainer_WriteBackLabelsSurviveFinalise(t *testing.T) {
	mock, u, stub := setupHookUpdate(t, map[string]string{
		"sentinel.hook.pre-update": "old backup from compose",
		"com.example.keep":         "yes",
	})
	mock.mirrorCreate = true
	stub.hooks["nginx"] = []hooks.Hook{
		{ContainerName: "nginx", Phase: "pre-update", Command: []string{"/bin/sh", "-c", "pg_dump -f /b.sql"}, Timeout: 120},
		{ContainerName: "nginx", Phase: "post-update", Command: []string{"/usr/bin/curl", "-sf", "http://localhost/health?a=1&b=2"}},
	}
	u.cfg.SetHooksWriteLabels(true)

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}

	if len(mock.createHistory) != 2 {
		t.Fatalf("creates = %d, want 2 (update, then finalise)", len(mock.createHistory))
	}
	want := map[string]string{
		"sentinel.hook.pre-update":  "pg_dump -f /b.sql",
		"sentinel.hook.post-update": "/usr/bin/curl -sf 'http://localhost/health?a=1&b=2'",
		"sentinel.hook.timeout":     "120",
		"com.example.keep":          "yes",
	}
	for i, stage := range []string{"update create", "finalise create"} {
		got := mock.createHistory[i].Labels
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: label %s = %q, want %q", stage, k, got[k], v)
			}
		}
	}
	if _, ok := mock.createHistory[1].Labels["sentinel.maintenance"]; ok {
		t.Error("finalise create still carries the maintenance label")
	}
	// The snapshot of the old container must not be touched.
	if got := mock.inspectResults["aaa"].Config.Labels["sentinel.hook.pre-update"]; got != "old backup from compose" {
		t.Errorf("old container labels changed: pre-update = %q", got)
	}
}

func TestUpdateContainer_NoWriteBackWhenDisabled(t *testing.T) {
	mock, u, stub := setupHookUpdate(t, map[string]string{})
	stub.hooks["nginx"] = []hooks.Hook{
		{ContainerName: "nginx", Phase: "pre-update", Command: []string{"/bin/sh", "-c", "pg_dump"}},
	}

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	if len(mock.createHistory) == 0 {
		t.Fatal("no container was created")
	}
	if _, ok := mock.createHistory[0].Labels["sentinel.hook.pre-update"]; ok {
		t.Error("hook label written while SENTINEL_HOOKS_WRITE_LABELS is off")
	}
}
