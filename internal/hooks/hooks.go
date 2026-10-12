package hooks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrSkipUpdate signals that a pre-update hook requested skipping the update (exit code 75).
var ErrSkipUpdate = errors.New("pre-update hook requested skip (exit 75)")

// Hook phases.
const (
	PhasePreUpdate  = "pre-update"
	PhasePostUpdate = "post-update"
)

// DefaultTimeout is the hook timeout in seconds when none is configured.
const DefaultTimeout = 30

// Where a resolved hook came from, as reported in the logs.
const (
	sourceStore = "store"
	sourceLabel = "label"
)

// Hook defines a lifecycle hook for a container.
type Hook struct {
	ContainerName string   `json:"container_name"`
	Phase         string   `json:"phase"`   // "pre-update" or "post-update"
	Command       []string `json:"command"` // e.g. ["/bin/sh", "-c", "pg_dump ..."]
	Timeout       int      `json:"timeout"` // seconds, default 30
}

// Store persists hook configurations.
type Store interface {
	ListHooks(containerName string) ([]Hook, error)
	SaveHook(hook Hook) error
	DeleteHook(containerName, phase string) error
}

// DockerExec is the subset of docker.API needed for exec.
type DockerExec interface {
	ExecContainer(ctx context.Context, id string, cmd []string, timeout int) (int, string, error)
}

// Runner executes lifecycle hooks.
type Runner struct {
	docker DockerExec
	store  Store
	log    *slog.Logger
}

// NewRunner creates a hook runner.
func NewRunner(docker DockerExec, store Store, log *slog.Logger) *Runner {
	return &Runner{docker: docker, store: store, log: log}
}

// RunPreUpdate executes the pre-update hooks for the given container. Hooks
// saved in the store for this phase win; otherwise the sentinel.hook.* labels
// are used. Returns ErrSkipUpdate if a hook exits with code 75, and an error
// for any other non-zero exit, exec failure or timeout.
func (r *Runner) RunPreUpdate(ctx context.Context, containerID, containerName string, labels map[string]string) error {
	return r.run(ctx, PhasePreUpdate, containerID, containerName, labels)
}

// RunPostUpdate executes the post-update hooks on the new container, resolved
// the same way as RunPreUpdate. Exit code 75 has no special meaning here.
func (r *Runner) RunPostUpdate(ctx context.Context, containerID, containerName string, labels map[string]string) error {
	return r.run(ctx, PhasePostUpdate, containerID, containerName, labels)
}

func (r *Runner) run(ctx context.Context, phase, containerID, containerName string, labels map[string]string) error {
	hooks, source, err := r.resolve(phase, containerName, labels)
	if err != nil {
		return err
	}
	if len(hooks) == 0 {
		r.log.Debug("no lifecycle hooks configured", "container", containerName, "phase", phase)
		return nil
	}

	for _, h := range hooks {
		timeout := h.Timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		r.log.Info("running lifecycle hook", "container", containerName, "phase", phase, "source", source, "timeout", timeout)

		execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		exitCode, output, err := r.docker.ExecContainer(execCtx, containerID, h.Command, timeout)
		timedOut := err != nil && errors.Is(execCtx.Err(), context.DeadlineExceeded)
		cancel()

		if timedOut {
			r.log.Warn("lifecycle hook timed out, stopped waiting (the command may still be running in the container)",
				"container", containerName, "phase", phase, "timeout", timeout)
			return fmt.Errorf("%s hook timed out after %ds: %w", phase, timeout, err)
		}
		if err != nil {
			r.log.Warn("lifecycle hook exec failed", "container", containerName, "phase", phase, "error", err)
			return fmt.Errorf("%s hook: %w", phase, err)
		}

		r.log.Info("lifecycle hook completed", "container", containerName, "phase", phase, "exit_code", exitCode, "output", output)

		if phase == PhasePreUpdate && exitCode == 75 {
			return ErrSkipUpdate
		}
		if exitCode != 0 {
			return fmt.Errorf("%s hook exited with code %d", phase, exitCode)
		}
	}
	return nil
}

// resolve returns the hooks to run for one phase and where they came from.
// A store entry for the phase wins over the container's labels.
func (r *Runner) resolve(phase, containerName string, labels map[string]string) ([]Hook, string, error) {
	stored, err := r.store.ListHooks(containerName)
	if err != nil {
		return nil, "", fmt.Errorf("list hooks: %w", err)
	}
	var hooks []Hook
	for _, h := range stored {
		if h.Phase == phase {
			hooks = append(hooks, h)
		}
	}
	if len(hooks) > 0 {
		return hooks, sourceStore, nil
	}

	r.warnLabelProblems(phase, containerName, labels)
	for _, h := range ReadLabels(containerName, labels) {
		if h.Phase == phase {
			hooks = append(hooks, h)
		}
	}
	return hooks, sourceLabel, nil
}

// warnLabelProblems logs label mistakes that would otherwise make a hook do
// nothing, or run with a different timeout, without a word.
func (r *Runner) warnLabelProblems(phase, containerName string, labels map[string]string) {
	key := phaseLabel(phase)
	cmd, ok := labels[key]
	if !ok {
		return
	}
	if isBlank(cmd) {
		r.log.Warn("hook label is set but has no command, so no hook runs; in a compose list write it as key=value",
			"container", containerName, "label", key)
		return
	}
	if v, ok := labels[labelTimeout]; ok {
		if _, valid := parseTimeout(v); !valid {
			r.log.Warn("invalid hook timeout label, using the default; give whole seconds",
				"container", containerName, "label", labelTimeout, "value", v, "default", DefaultTimeout)
		}
	}
}
