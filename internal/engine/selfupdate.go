package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/selfupdate"
	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
	"github.com/moby/moby/api/types/container"
)

// SelfUpdater replaces the running Sentinel container with one on a new image.
//
// When the Docker socket is mounted in this container, the swap is handed to a
// short-lived helper container (see package selfupdate): this process cannot
// free its own published ports or database lock before the new one starts.
// Without the socket (Sentinel reaches a remote daemon) it falls back to
// replacing in-process: rename, create, start the new container, then stop
// this one, which only works when no host ports are published.
type SelfUpdater struct {
	docker docker.API
	log    *logging.Logger

	updater *Updater // supplies the signature-verification gate
	socket  string   // Docker socket path in this container ("" = default)
	dataDir string   // directory of the database, for the helper's outcome file
	poll    time.Duration
}

// NewSelfUpdater creates a SelfUpdater.
func NewSelfUpdater(d docker.API, log *logging.Logger) *SelfUpdater {
	return &SelfUpdater{docker: d, log: log}
}

// SetUpdater attaches the Updater whose signature-verification mode and
// verifier gate the new Sentinel image.
func (su *SelfUpdater) SetUpdater(u *Updater) { su.updater = u }

// SetDockerSock sets the Docker socket path inside this container. A tcp://
// address means there is no socket to hand to a helper.
func (su *SelfUpdater) SetDockerSock(path string) { su.socket = path }

// SetDataDir sets the directory that holds the database, where the helper
// records the outcome for the new instance to report.
func (su *SelfUpdater) SetDataDir(dir string) { su.dataDir = dir }

// Update replaces the running Sentinel container with one on targetImage (or
// the container's current image when empty). On success it does not return:
// the helper stops this process.
func (su *SelfUpdater) Update(ctx context.Context, targetImage string) error {
	// 1. Find our own container.
	containers, err := su.docker.ListContainers(ctx)
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}

	var selfID, selfName string
	for _, c := range containers {
		if c.Labels["sentinel.self"] == "true" {
			selfID = c.ID
			if len(c.Names) > 0 {
				selfName = strings.TrimPrefix(c.Names[0], "/")
			}
			break
		}
	}
	if selfID == "" {
		return fmt.Errorf("could not find sentinel container (no sentinel.self=true label)")
	}

	// 2. Inspect to capture full config.
	inspect, err := su.docker.InspectContainer(ctx, selfID)
	if err != nil {
		return fmt.Errorf("inspect self: %w", err)
	}
	if inspect.Config == nil {
		return fmt.Errorf("inspect %s: container config is nil", selfName)
	}

	imageRef := inspect.Config.Image
	if targetImage != "" {
		imageRef = targetImage
	}
	su.log.Info("self-update initiated", "name", selfName, "image", imageRef)

	// 3. Signature gate for the new Sentinel image, before anything changes.
	if err := su.verifyTarget(ctx, inspect.Config.Labels, imageRef); err != nil {
		return err
	}

	// 4. Pull the new image before making any changes.
	// If the pull fails but the image is already on the host, carry on
	// (supports local builds and registry outages).
	su.log.Info("pulling target image", "image", imageRef)
	if err := su.docker.PullImage(ctx, imageRef); err != nil {
		if _, localErr := su.docker.ImageDigest(ctx, imageRef); localErr != nil {
			return fmt.Errorf("pull image: %w", err)
		}
		su.log.Warn("pull failed but image available locally", "image", imageRef, "pull_error", err)
	}

	defaults := selfupdate.ImageDefaults(ctx, su.docker, inspect.Image, su.log)

	// 5. Hand the swap to a helper container when the Docker socket is mounted.
	if !strings.HasPrefix(su.socket, "tcp://") && !strings.HasPrefix(su.socket, "tcps://") {
		h, err := selfupdate.Begin(ctx, selfupdate.Params{
			Docker: su.docker, Log: su.log, Inspect: inspect, Name: selfName, Image: imageRef,
			OldImageDefaults: defaults, DataDir: su.dataDir, Socket: su.socket, Poll: su.poll,
		})
		switch {
		case err == nil:
			su.log.Info("self-update: handed over to the helper container", "old", h.OldName())
			return h.Wait(context.Background())
		case !errors.Is(err, selfupdate.ErrNoSocket):
			return err
		}
		su.log.Info("self-update: the Docker socket is not mounted here, replacing in-process")
	}
	return su.updateInProcess(ctx, inspect, selfID, selfName, imageRef, defaults)
}

// verifyTarget applies the signature-verification gate to the new image: in
// enforce mode an image that fails, or a missing cosign, refuses the update;
// in warn mode the failure is logged.
func (su *SelfUpdater) verifyTarget(ctx context.Context, labels map[string]string, image string) error {
	if su.updater == nil {
		return nil
	}
	u := su.updater
	mode := u.verifyModeFor(labels)
	switch {
	case mode == verify.ModeDisabled:
		return nil
	case u.imgVerifier == nil && mode == verify.ModeEnforce:
		su.log.Warn("signature verification is set to enforce but cosign is not available, refusing the self-update", "image", image)
		return fmt.Errorf("self-update: %w", ErrVerifierUnavailable)
	case u.imgVerifier == nil:
		su.log.Warn("signature verification is set to warn but cosign is not available, self-updating without a signature check", "image", image)
		return nil
	}
	result := u.imgVerifier.Verify(ctx, image)
	switch {
	case result.Verified:
		su.log.Info("signature verified", "image", image)
	case mode == verify.ModeEnforce:
		su.log.Warn("signature verification failed, refusing the self-update", "image", image, "error", result.Error)
		return fmt.Errorf("signature verification failed for %s: %s", image, result.Error)
	default:
		su.log.Warn("signature verification failed, proceeding (warn mode)", "image", image, "error", result.Error)
	}
	return nil
}

// updateInProcess replaces this container without a helper: rename it aside,
// create and start the replacement while this one keeps serving, then stop
// this one. A failed start removes the replacement and restores the name.
// With published host ports the start fails and the update is rolled back.
func (su *SelfUpdater) updateInProcess(ctx context.Context, inspect container.InspectResponse, selfID, selfName, imageRef string, defaults *container.Config) error {
	oldName := fmt.Sprintf("%s-old-%d", selfName, time.Now().Unix())
	su.log.Info("renaming self", "from", selfName, "to", oldName)
	if err := su.docker.RenameContainer(ctx, selfID, oldName); err != nil {
		return fmt.Errorf("rename self: %w", err)
	}

	rc := selfupdate.BuildReplacement(inspect, imageRef, defaults)
	su.log.Info("creating replacement container", "name", selfName, "image", imageRef)
	newID, err := su.docker.CreateContainer(ctx, selfName, rc.Config, rc.HostConfig, rc.NetworkingConfig)
	if err != nil {
		su.log.Error("create failed, rolling back rename", "error", err)
		_ = su.docker.RenameContainer(ctx, selfID, selfName)
		return fmt.Errorf("create replacement: %w", err)
	}

	su.log.Info("starting replacement container", "id", newID[:12])
	if err := su.docker.StartContainer(ctx, newID); err != nil {
		su.log.Error("start failed, rolling back", "error", err)
		_ = su.docker.RemoveContainer(ctx, newID)
		_ = su.docker.RenameContainer(ctx, selfID, selfName)
		if isPortConflict(err) {
			return fmt.Errorf("start replacement: %w (the running instance still holds the published ports, and the Docker socket is not mounted here for the update helper; mount it, or publish no ports)", err)
		}
		return fmt.Errorf("start replacement: %w", err)
	}

	// Stop the old container, which is this process: it holds the database
	// lock. A background context is used because the caller's context may be
	// cancelled by the request that triggered the update.
	su.log.Info("self-update: new container running, stopping old container to release the database lock", "id", selfID)
	if err := su.docker.StopContainer(context.Background(), selfID, 10); err != nil {
		su.log.Error("self-update: failed to stop old container, rolling back", "error", err)
		_ = su.docker.RemoveContainer(context.Background(), newID)
		_ = su.docker.RenameContainer(context.Background(), selfID, selfName)
		return fmt.Errorf("replacement started but old container %s could not be stopped, rolled back: %w", oldName, err)
	}
	return nil
}

// isPortConflict reports whether a container start error is a host port
// collision, which happens when the running instance still publishes the port.
func isPortConflict(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "port is already allocated") || strings.Contains(msg, "address already in use")
}

// CleanupOldInstances removes containers left behind by earlier self-updates
// (see selfupdate.CleanupLeftovers).
func (su *SelfUpdater) CleanupOldInstances(ctx context.Context) {
	selfupdate.CleanupLeftovers(ctx, su.docker, su.dataDir, su.log)
}
