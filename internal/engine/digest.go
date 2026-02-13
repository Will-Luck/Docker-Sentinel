package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// DigestStore reads notification state for digest compilation.
type DigestStore interface {
	AllNotifyStates() (map[string]*store.NotifyState, error)
	AllNotifyPrefs() (map[string]*store.NotifyPref, error)
	LoadSetting(key string) (string, error)
	SaveSetting(key, value string) error
}

// DigestScheduler sends consolidated "pending updates" notifications on a schedule.
type DigestScheduler struct {
	store    DigestStore
	queue    *Queue
	notifier *notify.Multi
	events   *events.Bus
	log      *logging.Logger
	clock    clock.Clock
	settings SettingsReader
	resetCh  chan struct{}
	lastRun  time.Time
}

// NewDigestScheduler creates a DigestScheduler.
func NewDigestScheduler(s DigestStore, q *Queue, notifier *notify.Multi, bus *events.Bus, log *logging.Logger, clk clock.Clock) *DigestScheduler {
	return &DigestScheduler{
		store:    s,
		queue:    q,
		notifier: notifier,
		events:   bus,
		log:      log,
		clock:    clk,
		resetCh:  make(chan struct{}, 1),
	}
}

// SetSettingsReader attaches a settings reader for runtime config checks.
func (d *DigestScheduler) SetSettingsReader(sr SettingsReader) {
	d.settings = sr
}

// Run starts the digest loop. It calculates the time until the next digest fire,
// sleeps until then, fires the digest, then repeats. Exits when ctx is cancelled.
func (d *DigestScheduler) Run(ctx context.Context) error {
	for {
		if !d.isEnabled() {
			select {
			case <-d.clock.After(1 * time.Minute):
				continue
			case <-d.resetCh:
				d.log.Info("digest config changed, rechecking")
				continue
			case <-ctx.Done():
				d.log.Info("digest scheduler stopped")
				return nil
			}
		}

		delay := d.timeUntilNext()
		d.log.Info("digest scheduled", "delay", delay)

		select {
		case <-d.clock.After(delay):
			d.fire(ctx)
		case <-d.resetCh:
			d.log.Info("digest config changed, resetting timer")
			continue
		case <-ctx.Done():
			d.log.Info("digest scheduler stopped")
			return nil
		}
	}
}

// SetDigestConfig signals the scheduler to recalculate its timer.
func (d *DigestScheduler) SetDigestConfig() {
	select {
	case d.resetCh <- struct{}{}:
	default:
	}
}

// TriggerDigest runs an immediate digest outside the normal timer.
func (d *DigestScheduler) TriggerDigest(ctx context.Context) {
	d.log.Info("manual digest triggered")
	d.fire(ctx)
}

// LastRunTime returns when the last digest was sent.
func (d *DigestScheduler) LastRunTime() time.Time {
	return d.lastRun
}

// fire collects pending updates and sends a consolidated notification.
func (d *DigestScheduler) fire(ctx context.Context) {
	states, err := d.store.AllNotifyStates()
	if err != nil {
		d.log.Error("digest: failed to load notify states", "error", err)
		return
	}

	prefs, err := d.store.AllNotifyPrefs()
	if err != nil {
		d.log.Error("digest: failed to load notify prefs", "error", err)
		return
	}

	// Collect containers with pending updates, excluding muted ones.
	seen := make(map[string]bool)
	for name := range states {
		if d.effectiveMode(name, prefs) != "muted" {
			seen[name] = true
		}
	}

	// Also include manual queue entries.
	for _, item := range d.queue.List() {
		if d.effectiveMode(item.ContainerName, prefs) != "muted" {
			seen[item.ContainerName] = true
		}
	}

	d.lastRun = d.clock.Now()

	if len(seen) == 0 {
		d.log.Info("digest: no pending updates")
		return
	}

	// Sort for deterministic output.
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)

	msg := fmt.Sprintf("Pending updates: %s (%d containers awaiting action)", strings.Join(names, ", "), len(names))
	d.log.Info("sending digest", "containers", len(names))

	d.notifier.Notify(ctx, notify.Event{
		Type:           notify.EventDigest,
		ContainerName:  names[0],
		ContainerNames: names,
		Timestamp:      d.clock.Now(),
	})

	if d.events != nil {
		d.events.Publish(events.SSEEvent{
			Type:      events.EventDigestReady,
			Message:   msg,
			Timestamp: d.clock.Now(),
		})
	}
}

// isEnabled checks the digest_enabled setting (defaults to true).
func (d *DigestScheduler) isEnabled() bool {
	if d.settings == nil {
		return true
	}
	val, err := d.settings.LoadSetting("digest_enabled")
	if err != nil || val == "" {
		return true
	}
	return val != "false"
}

// digestTime returns the configured daily fire time (default 09:00).
func (d *DigestScheduler) digestTime() (int, int) {
	if d.settings == nil {
		return 9, 0
	}
	val, err := d.settings.LoadSetting("digest_time")
	if err != nil || val == "" {
		return 9, 0
	}
	t, err := time.Parse("15:04", val)
	if err != nil {
		return 9, 0
	}
	return t.Hour(), t.Minute()
}

// digestInterval returns the configured interval between digests (default 24h).
func (d *DigestScheduler) digestInterval() time.Duration {
	if d.settings == nil {
		return 24 * time.Hour
	}
	val, err := d.settings.LoadSetting("digest_interval")
	if err != nil || val == "" {
		return 24 * time.Hour
	}
	dur, err := time.ParseDuration(val)
	if err != nil || dur < 1*time.Hour {
		return 24 * time.Hour
	}
	return dur
}

// timeUntilNext calculates the duration until the next digest fire.
func (d *DigestScheduler) timeUntilNext() time.Duration {
	now := d.clock.Now()
	hour, min := d.digestTime()

	next := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, now.Location())
	if !now.Before(next) {
		next = next.Add(d.digestInterval())
	}

	delay := next.Sub(now)
	if delay < 0 {
		delay = 1 * time.Minute
	}
	return delay
}

// effectiveMode returns the notification mode for a container,
// falling back to the global default_notify_mode setting.
func (d *DigestScheduler) effectiveMode(name string, prefs map[string]*store.NotifyPref) string {
	if p, ok := prefs[name]; ok && p.Mode != "" && p.Mode != "default" {
		return p.Mode
	}
	if d.settings != nil {
		val, err := d.settings.LoadSetting("default_notify_mode")
		if err == nil && val != "" {
			return val
		}
	}
	return "default"
}
