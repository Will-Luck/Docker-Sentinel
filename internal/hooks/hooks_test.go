package hooks

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

type mockExec struct {
	calls   []execCall
	results map[string]execResult
	err     map[string]error
}

type execCall struct {
	id  string
	cmd []string
}

type execResult struct {
	exitCode int
	output   string
}

func newMockExec() *mockExec {
	return &mockExec{
		results: make(map[string]execResult),
		err:     make(map[string]error),
	}
}

func (m *mockExec) ExecContainer(_ context.Context, id string, cmd []string, _ int) (int, string, error) {
	m.calls = append(m.calls, execCall{id: id, cmd: cmd})
	if err, ok := m.err[id]; ok {
		return -1, "", err
	}
	if r, ok := m.results[id]; ok {
		return r.exitCode, r.output, nil
	}
	return 0, "", nil
}

type mockStore struct {
	hooks map[string][]Hook
}

func newMockStore() *mockStore {
	return &mockStore{hooks: make(map[string][]Hook)}
}

func (m *mockStore) ListHooks(name string) ([]Hook, error) {
	return m.hooks[name], nil
}

func (m *mockStore) SaveHook(h Hook) error {
	m.hooks[h.ContainerName] = append(m.hooks[h.ContainerName], h)
	return nil
}

func (m *mockStore) DeleteHook(name, phase string) error {
	filtered := m.hooks[name][:0]
	for _, h := range m.hooks[name] {
		if h.Phase != phase {
			filtered = append(filtered, h)
		}
	}
	m.hooks[name] = filtered
	return nil
}

func TestPreUpdateRuns(t *testing.T) {
	exec := newMockExec()
	store := newMockStore()
	store.hooks["nginx"] = []Hook{
		{ContainerName: "nginx", Phase: "pre-update", Command: []string{"/bin/sh", "-c", "echo pre"}, Timeout: 10},
	}

	runner := NewRunner(exec, store, slog.Default())
	err := runner.RunPreUpdate(context.Background(), "abc123", "nginx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(exec.calls))
	}
	if exec.calls[0].id != "abc123" {
		t.Errorf("expected exec on abc123, got %s", exec.calls[0].id)
	}
}

func TestPreUpdateExitCode75Skips(t *testing.T) {
	exec := newMockExec()
	exec.results["abc123"] = execResult{exitCode: 75, output: "skip requested"}
	store := newMockStore()
	store.hooks["nginx"] = []Hook{
		{ContainerName: "nginx", Phase: "pre-update", Command: []string{"/bin/sh", "-c", "check"}, Timeout: 10},
	}

	runner := NewRunner(exec, store, slog.Default())
	err := runner.RunPreUpdate(context.Background(), "abc123", "nginx")
	if !errors.Is(err, ErrSkipUpdate) {
		t.Errorf("expected ErrSkipUpdate, got %v", err)
	}
}

func TestPostUpdateRuns(t *testing.T) {
	exec := newMockExec()
	store := newMockStore()
	store.hooks["nginx"] = []Hook{
		{ContainerName: "nginx", Phase: "post-update", Command: []string{"/bin/sh", "-c", "echo post"}, Timeout: 10},
	}

	runner := NewRunner(exec, store, slog.Default())
	err := runner.RunPostUpdate(context.Background(), "new123", "nginx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(exec.calls))
	}
}

func TestPreUpdateExecError(t *testing.T) {
	exec := newMockExec()
	exec.err["abc123"] = errors.New("connection refused")
	store := newMockStore()
	store.hooks["nginx"] = []Hook{
		{ContainerName: "nginx", Phase: "pre-update", Command: []string{"/bin/sh", "-c", "check"}, Timeout: 10},
	}

	runner := NewRunner(exec, store, slog.Default())
	err := runner.RunPreUpdate(context.Background(), "abc123", "nginx")
	if err == nil {
		t.Error("expected error")
	}
}

func TestNoHooksIsNoop(t *testing.T) {
	exec := newMockExec()
	store := newMockStore()

	runner := NewRunner(exec, store, slog.Default())
	if err := runner.RunPreUpdate(context.Background(), "abc123", "nginx"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(exec.calls) != 0 {
		t.Error("expected no exec calls for container with no hooks")
	}
}
