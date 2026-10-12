package hooks

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

type mockExec struct {
	calls   []execCall
	results map[string]execResult
	err     map[string]error
	block   map[string]bool // wait for the context to end, like a hung command
}

type execCall struct {
	id      string
	cmd     []string
	timeout int
}

type execResult struct {
	exitCode int
	output   string
}

func newMockExec() *mockExec {
	return &mockExec{
		results: make(map[string]execResult),
		err:     make(map[string]error),
		block:   make(map[string]bool),
	}
}

func (m *mockExec) ExecContainer(ctx context.Context, id string, cmd []string, timeout int) (int, string, error) {
	m.calls = append(m.calls, execCall{id: id, cmd: cmd, timeout: timeout})
	if m.block[id] {
		<-ctx.Done()
		return -1, "", ctx.Err()
	}
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

// captureLog returns a logger that writes text records at Debug and above to buf.
func captureLog(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestPreUpdateRuns(t *testing.T) {
	exec := newMockExec()
	store := newMockStore()
	store.hooks["nginx"] = []Hook{
		{ContainerName: "nginx", Phase: "pre-update", Command: []string{"/bin/sh", "-c", "echo pre"}, Timeout: 10},
	}

	runner := NewRunner(exec, store, slog.Default())
	err := runner.RunPreUpdate(context.Background(), "abc123", "nginx", nil)
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
	err := runner.RunPreUpdate(context.Background(), "abc123", "nginx", nil)
	if !errors.Is(err, ErrSkipUpdate) {
		t.Errorf("expected ErrSkipUpdate, got %v", err)
	}
}

func TestPreUpdateNonZeroExitFails(t *testing.T) {
	exec := newMockExec()
	exec.results["abc123"] = execResult{exitCode: 1, output: "backup failed"}
	labels := map[string]string{"sentinel.hook.pre-update": "backup"}

	runner := NewRunner(exec, newMockStore(), slog.Default())
	err := runner.RunPreUpdate(context.Background(), "abc123", "nginx", labels)
	if err == nil {
		t.Fatal("expected an error for exit code 1")
	}
	if errors.Is(err, ErrSkipUpdate) {
		t.Error("exit code 1 must not be treated as a skip")
	}
}

func TestPostUpdateRuns(t *testing.T) {
	exec := newMockExec()
	store := newMockStore()
	store.hooks["nginx"] = []Hook{
		{ContainerName: "nginx", Phase: "post-update", Command: []string{"/bin/sh", "-c", "echo post"}, Timeout: 10},
	}

	runner := NewRunner(exec, store, slog.Default())
	err := runner.RunPostUpdate(context.Background(), "new123", "nginx", nil)
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
	err := runner.RunPreUpdate(context.Background(), "abc123", "nginx", nil)
	if err == nil {
		t.Error("expected error")
	}
}

func TestPreUpdateTimeoutFails(t *testing.T) {
	exec := newMockExec()
	exec.block["abc123"] = true
	labels := map[string]string{
		"sentinel.hook.pre-update": "sleep 600",
		"sentinel.hook.timeout":    "1",
	}

	var buf bytes.Buffer
	runner := NewRunner(exec, newMockStore(), captureLog(&buf))
	err := runner.RunPreUpdate(context.Background(), "abc123", "nginx", labels)
	if err == nil {
		t.Fatal("expected an error when the hook times out")
	}
	if errors.Is(err, ErrSkipUpdate) {
		t.Error("a timeout must not be treated as a skip")
	}
	if !strings.Contains(err.Error(), "timed out after 1s") {
		t.Errorf("error = %q, want it to say the hook timed out after 1s", err)
	}
}

func TestNoHooksIsNoop(t *testing.T) {
	exec := newMockExec()
	store := newMockStore()

	var buf bytes.Buffer
	runner := NewRunner(exec, store, captureLog(&buf))
	if err := runner.RunPreUpdate(context.Background(), "abc123", "nginx", nil); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(exec.calls) != 0 {
		t.Error("expected no exec calls for container with no hooks")
	}
	out := buf.String()
	if !strings.Contains(out, "level=DEBUG") || !strings.Contains(out, "no lifecycle hooks") {
		t.Errorf("expected a debug line saying no hooks were found, got:\n%s", out)
	}
}

func TestPreUpdateRunsLabelHook(t *testing.T) {
	exec := newMockExec()
	labels := map[string]string{
		"sentinel.hook.pre-update": "wget -qO- http://api.example/notify",
		"sentinel.hook.timeout":    "45",
	}

	var buf bytes.Buffer
	runner := NewRunner(exec, newMockStore(), captureLog(&buf))
	if err := runner.RunPreUpdate(context.Background(), "abc123", "app", labels); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(exec.calls))
	}
	call := exec.calls[0]
	want := []string{"/bin/sh", "-c", "wget -qO- http://api.example/notify"}
	if strings.Join(call.cmd, "|") != strings.Join(want, "|") {
		t.Errorf("cmd = %q, want %q", call.cmd, want)
	}
	if call.id != "abc123" {
		t.Errorf("exec on %q, want abc123", call.id)
	}
	if call.timeout != 45 {
		t.Errorf("timeout = %d, want 45", call.timeout)
	}
	out := buf.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "source=label") || !strings.Contains(out, "phase=pre-update") {
		t.Errorf("expected an info line naming the phase and label source, got:\n%s", out)
	}
}

func TestPostUpdateRunsLabelHook(t *testing.T) {
	exec := newMockExec()
	labels := map[string]string{"sentinel.hook.post-update": "curl -sf http://localhost/health"}

	runner := NewRunner(exec, newMockStore(), slog.Default())
	if err := runner.RunPostUpdate(context.Background(), "new123", "app", labels); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exec.calls) != 1 || exec.calls[0].id != "new123" {
		t.Fatalf("expected one exec on new123, got %+v", exec.calls)
	}
	if exec.calls[0].timeout != DefaultTimeout {
		t.Errorf("timeout = %d, want default %d", exec.calls[0].timeout, DefaultTimeout)
	}
}

func TestStoreHookWinsOverLabelForSamePhase(t *testing.T) {
	exec := newMockExec()
	store := newMockStore()
	store.hooks["app"] = []Hook{
		{ContainerName: "app", Phase: "pre-update", Command: []string{"/usr/local/bin/backup"}, Timeout: 10},
	}
	labels := map[string]string{"sentinel.hook.pre-update": "echo from-label"}

	var buf bytes.Buffer
	runner := NewRunner(exec, store, captureLog(&buf))
	if err := runner.RunPreUpdate(context.Background(), "abc123", "app", labels); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(exec.calls))
	}
	if got := strings.Join(exec.calls[0].cmd, " "); got != "/usr/local/bin/backup" {
		t.Errorf("ran %q, want the store hook", got)
	}
	if !strings.Contains(buf.String(), "source=store") {
		t.Errorf("expected source=store in the log, got:\n%s", buf.String())
	}
}

func TestStoreHookOnlyCoversItsOwnPhase(t *testing.T) {
	exec := newMockExec()
	store := newMockStore()
	store.hooks["app"] = []Hook{
		{ContainerName: "app", Phase: "post-update", Command: []string{"/bin/true"}, Timeout: 10},
	}
	labels := map[string]string{"sentinel.hook.pre-update": "echo from-label"}

	runner := NewRunner(exec, store, slog.Default())
	if err := runner.RunPreUpdate(context.Background(), "abc123", "app", labels); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exec.calls) != 1 || exec.calls[0].cmd[2] != "echo from-label" {
		t.Fatalf("expected the pre-update label hook to run, got %+v", exec.calls)
	}
}

func TestEmptyHookLabelWarns(t *testing.T) {
	exec := newMockExec()
	// Compose list form "- sentinel.hook.pre-update" with no "=value" yields "".
	labels := map[string]string{"sentinel.hook.pre-update": ""}

	var buf bytes.Buffer
	runner := NewRunner(exec, newMockStore(), captureLog(&buf))
	if err := runner.RunPreUpdate(context.Background(), "abc123", "app", labels); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exec.calls) != 0 {
		t.Errorf("an empty label must not run anything, got %+v", exec.calls)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "sentinel.hook.pre-update") || !strings.Contains(out, "key=value") {
		t.Errorf("expected a warning naming the empty label and the key=value form, got:\n%s", out)
	}
}

func TestInvalidTimeoutLabelWarnsAndUsesDefault(t *testing.T) {
	for _, v := range []string{"2m", "abc", "0", "-5"} {
		t.Run(v, func(t *testing.T) {
			exec := newMockExec()
			labels := map[string]string{
				"sentinel.hook.pre-update": "echo hi",
				"sentinel.hook.timeout":    v,
			}

			var buf bytes.Buffer
			runner := NewRunner(exec, newMockStore(), captureLog(&buf))
			if err := runner.RunPreUpdate(context.Background(), "abc123", "app", labels); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(exec.calls) != 1 || exec.calls[0].timeout != DefaultTimeout {
				t.Fatalf("expected one exec with the default timeout, got %+v", exec.calls)
			}
			out := buf.String()
			if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "sentinel.hook.timeout") {
				t.Errorf("expected a warning about the invalid timeout, got:\n%s", out)
			}
		})
	}
}
