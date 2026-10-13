package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
// ErrVerifierUnavailable. It does not notify: a scan sends one notification
// for all the updates it had to refuse (notifyUnverifiable).
func (u *Updater) refuseUnverifiable(name, oldImage, newImage, oldDigest string, start time.Time) {
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
	}); err != nil {
		u.log.Warn("failed to persist the refused update", "name", name, "error", err)
	}
	metrics.UpdatesTotal.WithLabelValues("failed").Inc()
	u.publishEvent(events.EventContainerUpdate, name, "update failed: "+ErrVerifierUnavailable.Error())
}

// notifyUnverifiable sends one notification for the updates a scan refused
// with ErrVerifierUnavailable.
func (u *Updater) notifyUnverifiable(ctx context.Context, names []string) {
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
