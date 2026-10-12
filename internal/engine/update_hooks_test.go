package engine

import (
	"context"
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
