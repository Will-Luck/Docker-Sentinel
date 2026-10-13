package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/metrics"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
)

// ErrVerifierUnavailable is returned for an update whose image signature
// must be checked (enforce mode) when no verifier is attached: cosign was
// not found when Sentinel started.
var ErrVerifierUnavailable = errors.New("signature verification is set to enforce but cosign is not available")

// unverifiableError is ErrVerifierUnavailable for one container or
// service, so a scan can name the updates it refused.
type unverifiableError struct{ name string }

func (e *unverifiableError) Error() string {
	return "update " + e.name + ": " + ErrVerifierUnavailable.Error()
}
func (e *unverifiableError) Unwrap() error { return ErrVerifierUnavailable }

// verifyModeFor returns the signature verification mode for a container:
// its sentinel.verify label first, then the global mode. The global mode is
// read from the settings each time, so a change saved in the dashboard
// applies without a restart; the mode set with SetVerifyMode is used when
// there are no settings to read.
func (u *Updater) verifyModeFor(labels map[string]string) verify.Mode {
	global := u.verifyMode
	if u.settings != nil {
		if v, err := u.settings.LoadSetting(store.SettingVerifyMode); err == nil {
			global = verify.ParseMode(v)
		}
	}
	return verify.ResolveMode(labels[verify.ContainerLabel], "", global)
}

// refuseUnverifiable logs and records an update refused with
// ErrVerifierUnavailable, and returns the error to give back. typ is the
// history row type ("" for a container, "service"). It does not notify: a
// scan sends one notification for all the updates it had to refuse
// (notifyUnverifiable).
func (u *Updater) refuseUnverifiable(name, oldImage, newImage, oldDigest, typ string, start time.Time) error {
	u.log.Warn("signature verification is set to enforce but cosign is not available, refusing the update",
		"name", name, "image", newImage)
	if err := u.store.RecordUpdate(store.UpdateRecord{
		Timestamp:     u.clock.Now(),
		ContainerName: name,
		OldImage:      oldImage,
		OldDigest:     oldDigest,
		NewImage:      newImage,
		Outcome:       "failed",
		Duration:      u.clock.Since(start),
		Error:         ErrVerifierUnavailable.Error(),
		Type:          typ,
	}); err != nil {
		u.log.Warn("failed to persist the refused update", "name", name, "error", err)
	}
	metrics.UpdatesTotal.WithLabelValues("failed").Inc()
	evt := events.EventContainerUpdate
	if typ == "service" {
		evt = events.EventServiceUpdate
	}
	u.publishEvent(evt, name, "update failed: "+ErrVerifierUnavailable.Error())
	return &unverifiableError{name: name}
}

// checkSignature verifies image under mode (an enforced check without a
// verifier is refused before this). It returns an error when an enforced
// check fails; in warn mode a failed or impossible check is only logged.
func (u *Updater) checkSignature(ctx context.Context, name, image string, mode verify.Mode) error {
	switch {
	case mode == verify.ModeDisabled:
		return nil
	case u.imgVerifier == nil:
		u.log.Warn("signature verification is set to warn but cosign is not available, updating without a signature check",
			"name", name, "image", image)
		return nil
	}
	result := u.imgVerifier.Verify(ctx, image)
	switch {
	case result.Verified:
		u.log.Info("signature verified", "name", name, "image", image)
	case mode == verify.ModeEnforce:
		u.log.Warn("signature verification failed, blocking update", "name", name, "image", image, "error", result.Error)
		return fmt.Errorf("signature verification failed for %s: %s", name, result.Error)
	default:
		u.log.Warn("signature verification failed, proceeding (warn mode)", "name", name, "image", image, "error", result.Error)
	}
	return nil
}

// signatureRef returns the reference whose signature an update checks: the
// pulled image's digest in its own repository, which is what the container
// is created from, so the registry cannot move the tag between the check
// and the create. When no such digest is known it is image itself.
func (u *Updater) signatureRef(ctx context.Context, image string) string {
	r, ok := u.docker.(docker.RepoDigestReader)
	if !ok {
		return image
	}
	digests, err := r.ImageRepoDigests(ctx, image)
	if err != nil {
		return image
	}
	repo := repoKey(image)
	for _, d := range digests {
		if name, _, ok := strings.Cut(d, "@"); ok && repoKey(name) == repo {
			return d
		}
	}
	return image
}

// notifyUnverifiable sends one notification for the updates a scan refused
// with ErrVerifierUnavailable, found in its errors.
func (u *Updater) notifyUnverifiable(ctx context.Context, errs []error) {
	var names []string
	for _, err := range errs {
		if ue, ok := errors.AsType[*unverifiableError](err); ok {
			names = append(names, ue.name)
		}
	}
	if len(names) == 0 {
		return
	}
	u.notifier.Notify(ctx, notify.Event{
		Type:           notify.EventUpdateFailed,
		ContainerName:  strings.Join(names, ", "),
		ContainerNames: names,
		Error: fmt.Sprintf("%s, so %d update(s) were refused. Make cosign available to Sentinel "+
			"(and set its path in the signature verification settings), or change the mode.",
			ErrVerifierUnavailable, len(names)),
		Timestamp: u.clock.Now(),
	})
}
