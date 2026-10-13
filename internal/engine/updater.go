package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker/imageconfig"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/scanner"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
)

// ErrUpdateInProgress is returned when an update is attempted on a container
// that already has an update in progress.
var ErrUpdateInProgress = fmt.Errorf("update already in progress")

// ErrRolledBack is wrapped by the error UpdateContainer returns when the
// update changed the container and was rolled back (the new container
// failed to start or to validate). Scan then leaves that exact update alone
// until a new version or digest appears.
var ErrRolledBack = errors.New("update rolled back")

// ImageScanner scans a container image for vulnerabilities.
type ImageScanner interface {
	Scan(ctx context.Context, imageRef string) (*scanner.ScanResult, error)
}

// ImageVerifier verifies the signature of a container image.
type ImageVerifier interface {
	Verify(ctx context.Context, imageRef string) *verify.Result
}

// finaliseError wraps an error with the stage at which finaliseContainer failed.
// Stage values: "inspect", or a swap stage ("rename", "create", "verify", "start").
type finaliseError struct {
	stage string
	err   error
	// down is set when the validated container is gone after the swap
	// failed, so only a rebuild can bring the service back.
	down bool
	// stuck is set when the validated container still exists but could not
	// be put back under its name; it is reported, never rebuilt next to.
	stuck bool
}

func (e *finaliseError) Error() string { return fmt.Sprintf("finalise %s: %v", e.stage, e.err) }
func (e *finaliseError) Unwrap() error { return e.err }

// Updater performs container scanning and update operations.
type Updater struct {
	docker           docker.API
	checker          *registry.Checker
	store            *store.Store
	queue            *Queue
	cfg              *config.Config
	log              *logging.Logger
	clock            clock.Clock
	notifier         *notify.Multi
	events           *events.Bus
	settings         SettingsReader
	rateTracker      *registry.RateLimitTracker // optional: rate limit awareness
	rateSaver        func([]byte) error         // optional: persist rate limits after scan
	ghcrCache        *registry.GHCRCache        // optional: GHCR alternative detection cache
	ghcrSaver        func([]byte) error         // optional: persist GHCR cache after checks
	updating         sync.Map                   // map[string]*sync.Mutex: per-container update locks
	activeUpdates    atomic.Int32               // tracks number of in-progress updates for IsIdle()
	hooks            *hooks.Runner              // optional: lifecycle hook runner
	cluster          ClusterScanner             // optional: nil = single-host mode
	haDiscovery      *notify.HADiscovery        // optional: HA MQTT auto-discovery publisher
	imgScanner       ImageScanner               // optional: trivy vulnerability scanner
	scannerFactory   ScannerFactory             // optional: builds the scanner from the saved Trivy path
	scanCache        scannerCache
	imgVerifier      ImageVerifier        // optional: cosign signature verifier
	scanMode         scanner.ScanMode     // disabled/pre-update/post-update
	verifyMode       verify.Mode          // disabled/warn/enforce
	severityThresh   scanner.Severity     // block threshold for pre-update scans
	ghcrWg           sync.WaitGroup       // tracks background GHCR alternative checks
	ghcrRunning      atomic.Bool          // prevents concurrent GHCR checks
	ghcrCancel       context.CancelFunc   // cancels the running GHCR check on shutdown
	selfUpdateQueued atomic.Bool          // set when a self-update is queued during scan
	selfUpdateKey    atomic.Value         // stores queue key (string) of the self-update entry
	shutdownCtx      context.Context      // optional: ends every update and rollback on shutdown
	imageConfigs     *imageconfig.Fetcher // configs of images containers ran before (inheritedImages)
	watching         atomic.Bool          // the watch of restored containers is running (watchRestored)
	watchers         sync.WaitGroup       // tracks that watch
}

// NewUpdater creates an Updater with all dependencies.
func NewUpdater(d docker.API, checker *registry.Checker, s *store.Store, q *Queue, cfg *config.Config, log *logging.Logger, clk clock.Clock, notifier *notify.Multi, bus *events.Bus) *Updater {
	u := &Updater{
		docker:   d,
		checker:  checker,
		store:    s,
		queue:    q,
		cfg:      cfg,
		log:      log,
		clock:    clk,
		notifier: notifier,
		events:   bus,
	}
	local, _ := d.(docker.ImageConfigReader)
	u.imageConfigs = imageconfig.New(local, u.registryCredentials)
	return u
}

// SetSettingsReader attaches a settings reader for runtime filter checks.
func (u *Updater) SetSettingsReader(sr SettingsReader) {
	u.settings = sr
}

// SetRateLimitTracker attaches a rate limit tracker for scan pacing.
func (u *Updater) SetRateLimitTracker(t *registry.RateLimitTracker) {
	u.rateTracker = t
}

// SetRateLimitSaver attaches a function to persist rate limits after each scan.
func (u *Updater) SetRateLimitSaver(fn func([]byte) error) {
	u.rateSaver = fn
}

// SetGHCRCache attaches a GHCR alternative detection cache.
func (u *Updater) SetGHCRCache(c *registry.GHCRCache) {
	u.ghcrCache = c
}

// SetGHCRSaver attaches a function to persist the GHCR cache after checks.
func (u *Updater) SetGHCRSaver(fn func([]byte) error) {
	u.ghcrSaver = fn
}

// SetHookRunner attaches a lifecycle hook runner.
func (u *Updater) SetHookRunner(r *hooks.Runner) {
	u.hooks = r
}

// SetClusterScanner attaches a cluster scanner for multi-host scanning.
// When nil (the default), remote host scanning is skipped entirely.
func (u *Updater) SetClusterScanner(cs ClusterScanner) {
	u.cluster = cs
}

// SetHADiscovery attaches an HA MQTT auto-discovery publisher.
func (u *Updater) SetHADiscovery(h *notify.HADiscovery) {
	u.haDiscovery = h
}

// SetScanner attaches a vulnerability scanner (Trivy).
func (u *Updater) SetScanner(s ImageScanner) {
	u.imgScanner = s
}

// SetVerifier attaches a signature verifier (cosign).
func (u *Updater) SetVerifier(v ImageVerifier) {
	u.imgVerifier = v
}

// SetScanMode sets when vulnerability scanning runs.
func (u *Updater) SetScanMode(m scanner.ScanMode) {
	u.scanMode = m
}

// SetVerifyMode sets the signature verification behaviour used when there
// are no settings to read; with a settings reader, the saved mode applies
// (see verifyModeFor).
func (u *Updater) SetVerifyMode(m verify.Mode) {
	u.verifyMode = m
}

// SetSeverityThreshold sets the minimum severity that blocks a pre-update scan.
func (u *Updater) SetSeverityThreshold(s scanner.Severity) {
	u.severityThresh = s
}

// SetShutdownContext makes every update and rollback end when ctx ends, as
// a shutdown does, even when it was started with a context of its own (the
// dashboard starts them detached from the request). An update ended part
// way puts the old container back. Call it before any update can start.
func (u *Updater) SetShutdownContext(ctx context.Context) {
	u.shutdownCtx = ctx
}

// withShutdown returns ctx, also cancelled when the shutdown context ends.
func (u *Updater) withShutdown(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	if u.shutdownCtx == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(u.shutdownCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// WaitIdle waits until no update or rollback is in progress, or ctx ends,
// and reports whether they all finished.
func (u *Updater) WaitIdle(ctx context.Context) bool {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for !u.IsIdle() {
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
	return true
}

// Close cancels any running background work and waits for goroutines to finish.
func (u *Updater) Close() {
	if u.ghcrCancel != nil {
		u.ghcrCancel()
	}
	u.ghcrWg.Wait()
}

// tryLock attempts to acquire the per-container update lock.
// Returns false if the container already has an update in progress.
func (u *Updater) tryLock(name string) bool {
	mu := &sync.Mutex{}
	actual, _ := u.updating.LoadOrStore(name, mu)
	if actual.(*sync.Mutex).TryLock() {
		u.activeUpdates.Add(1)
		return true
	}
	return false
}

// unlock releases the per-container update lock and removes the entry
// from the map atomically to prevent stale mutex accumulation.
// LoadAndDelete is atomic — no window for another goroutine to LoadOrStore
// between our Unlock and Delete (which was the previous race condition).
func (u *Updater) unlock(name string) {
	if val, ok := u.updating.LoadAndDelete(name); ok {
		val.(*sync.Mutex).Unlock()
		u.activeUpdates.Add(-1)
	}
}

// IsUpdating reports whether a container currently has an update in progress.
func (u *Updater) IsUpdating(name string) bool {
	val, ok := u.updating.Load(name)
	if !ok {
		return false
	}
	mu := val.(*sync.Mutex)
	if mu.TryLock() {
		mu.Unlock()
		return false
	}
	return true
}

// IsIdle returns true when no container updates are in progress.
func (u *Updater) IsIdle() bool {
	return u.activeUpdates.Load() == 0
}

// SelfUpdateQueued reports whether the last scan found a Sentinel self-update.
func (u *Updater) SelfUpdateQueued() bool {
	return u.selfUpdateQueued.Load()
}

// SelfUpdateKey returns the queue key of the queued self-update, if any.
func (u *Updater) SelfUpdateKey() string {
	v := u.selfUpdateKey.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}
