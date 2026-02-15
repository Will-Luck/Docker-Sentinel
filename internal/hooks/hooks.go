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

// RunPreUpdate executes pre-update hooks for the given container.
// Returns ErrSkipUpdate if any hook exits with code 75.
func (r *Runner) RunPreUpdate(ctx context.Context, containerID, containerName string) error {
	hooks, err := r.store.ListHooks(containerName)
	if err != nil {
		return fmt.Errorf("list hooks: %w", err)
	}

	for _, h := range hooks {
		if h.Phase != "pre-update" {
			continue
		}
		timeout := h.Timeout
		if timeout <= 0 {
			timeout = 30
		}

		execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		exitCode, output, err := r.docker.ExecContainer(execCtx, containerID, h.Command, timeout)
		cancel()

		if err != nil {
			r.log.Warn("pre-update hook exec failed", "container", containerName, "error", err)
			return fmt.Errorf("pre-update hook: %w", err)
		}

		r.log.Info("pre-update hook completed", "container", containerName, "exit_code", exitCode, "output", output)

		if exitCode == 75 {
			return ErrSkipUpdate
		}
		if exitCode != 0 {
			return fmt.Errorf("pre-update hook exited with code %d", exitCode)
		}
	}
	return nil
}

// RunPostUpdate executes post-update hooks on the new container.
func (r *Runner) RunPostUpdate(ctx context.Context, containerID, containerName string) error {
	hooks, err := r.store.ListHooks(containerName)
	if err != nil {
		return fmt.Errorf("list hooks: %w", err)
	}

	for _, h := range hooks {
		if h.Phase != "post-update" {
			continue
		}
		timeout := h.Timeout
		if timeout <= 0 {
			timeout = 30
		}

		execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		exitCode, output, err := r.docker.ExecContainer(execCtx, containerID, h.Command, timeout)
		cancel()

		if err != nil {
			r.log.Warn("post-update hook exec failed", "container", containerName, "error", err)
			return fmt.Errorf("post-update hook: %w", err)
		}

		r.log.Info("post-update hook completed", "container", containerName, "exit_code", exitCode, "output", output)

		if exitCode != 0 {
			return fmt.Errorf("post-update hook exited with code %d", exitCode)
		}
	}
	return nil
}
