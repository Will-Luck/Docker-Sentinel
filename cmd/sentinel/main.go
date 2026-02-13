package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/web"
)

var version = "dev"

func main() {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(1)
	}

	// Auto-detect CookieSecure when not explicitly set: secure only if TLS is enabled.
	if os.Getenv("SENTINEL_COOKIE_SECURE") == "" {
		cfg.CookieSecure = cfg.TLSEnabled()
	}
	log := logging.New(cfg.LogJSON)

	fmt.Println("Docker-Sentinel " + version)
	fmt.Println("=============================================")
	fmt.Printf("SENTINEL_POLL_INTERVAL=%s\n", cfg.PollInterval())
	fmt.Printf("SENTINEL_GRACE_PERIOD=%s\n", cfg.GracePeriod())
	fmt.Printf("SENTINEL_DEFAULT_POLICY=%s\n", cfg.DefaultPolicy())
	fmt.Printf("SENTINEL_DB_PATH=%s\n", cfg.DBPath)
	fmt.Printf("SENTINEL_WEB_ENABLED=%t\n", cfg.WebEnabled)
	fmt.Printf("SENTINEL_WEB_PORT=%s\n", cfg.WebPort)
	fmt.Printf("SENTINEL_TLS_CERT=%s\n", cfg.TLSCert)
	fmt.Printf("SENTINEL_TLS_KEY=%s\n", cfg.TLSKey)
	fmt.Printf("SENTINEL_TLS_AUTO=%t\n", cfg.TLSAuto)
	fmt.Printf("SENTINEL_WEBAUTHN_RPID=%s\n", cfg.WebAuthnRPID)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	client, err := docker.NewClient(cfg.DockerSock)
	if err != nil {
		log.Error("failed to create Docker client", "error", err)
		os.Exit(1)
	}
	defer client.Close()

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Initialise auth buckets and seed built-in roles.
	if err := db.EnsureAuthBuckets(); err != nil {
		log.Error("failed to create auth buckets", "error", err)
		os.Exit(1)
	}
	if err := db.SeedBuiltinRoles(); err != nil {
		log.Error("failed to seed built-in roles", "error", err)
		os.Exit(1)
	}

	// Create auth service.
	var webAuthnCreds auth.WebAuthnCredentialStore
	if cfg.WebAuthnEnabled() {
		webAuthnCreds = db
	}
	authSvc := auth.NewService(auth.ServiceConfig{
		Users:          db,
		Sessions:       db,
		Roles:          db,
		Tokens:         db,
		Settings:       db,
		WebAuthnCreds:  webAuthnCreds,
		Log:            log.Logger,
		CookieSecure:   cfg.CookieSecure,
		SessionExpiry:  cfg.SessionExpiry,
		AuthEnabledEnv: cfg.AuthEnabled,
	})

	// Generate bootstrap token for first-run setup if needed.
	var bootstrapToken string
	if authSvc.NeedsSetup() {
		bootstrapToken, err = auth.GenerateBootstrapToken()
		if err != nil {
			log.Error("failed to generate bootstrap token", "error", err)
			os.Exit(1)
		}
	}

	// Load persisted runtime settings (override env defaults).
	if saved, err := db.LoadSetting("poll_interval"); err == nil && saved != "" {
		if d, err := time.ParseDuration(saved); err == nil && d >= 5*time.Minute {
			cfg.SetPollInterval(d)
			log.Info("loaded persisted poll interval", "interval", d)
		}
	}
	if saved, err := db.LoadSetting("default_policy"); err == nil && saved != "" {
		switch saved {
		case "auto", "manual", "pinned":
			cfg.SetDefaultPolicy(saved)
			log.Info("loaded persisted default policy", "policy", saved)
		}
	}
	if saved, err := db.LoadSetting("grace_period"); err == nil && saved != "" {
		if d, err := time.ParseDuration(saved); err == nil && d >= 0 {
			cfg.SetGracePeriod(d)
			log.Info("loaded persisted grace period", "duration", d)
		}
	}

	// Build notification chain from persisted channels, with env var fallback.
	var notifiers []notify.Notifier
	notifiers = append(notifiers, notify.NewLogNotifier(log))

	channels, err := db.GetNotificationChannels()
	if err != nil {
		log.Warn("failed to load notification channels", "error", err)
	}

	if len(channels) == 0 {
		// Env var fallback: synthesise channels from SENTINEL_GOTIFY_URL etc.
		if cfg.GotifyURL != "" {
			settings, _ := json.Marshal(notify.GotifySettings{URL: cfg.GotifyURL, Token: cfg.GotifyToken})
			channels = append(channels, notify.Channel{
				ID: notify.GenerateID(), Type: notify.ProviderGotify,
				Name: "Gotify", Enabled: true, Settings: settings,
			})
			log.Info("gotify notifications enabled from env", "url", cfg.GotifyURL)
		}
		if cfg.WebhookURL != "" {
			headers := parseHeaders(cfg.WebhookHeaders)
			settings, _ := json.Marshal(notify.WebhookSettings{URL: cfg.WebhookURL, Headers: headers})
			channels = append(channels, notify.Channel{
				ID: notify.GenerateID(), Type: notify.ProviderWebhook,
				Name: "Webhook", Enabled: true, Settings: settings,
			})
			log.Info("webhook notifications enabled from env", "url", cfg.WebhookURL)
		}
	}

	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		n, buildErr := notify.BuildFilteredNotifier(ch)
		if buildErr != nil {
			log.Warn("failed to build notifier", "channel", ch.Name, "error", buildErr)
			continue
		}
		notifiers = append(notifiers, n)
		log.Info("notification channel enabled", "name", ch.Name, "type", string(ch.Type))
	}
	notifier := notify.NewMulti(log, notifiers...)

	clk := clock.Real{}
	checker := registry.NewChecker(client, log)
	rateTracker := registry.NewRateLimitTracker()
	checker.SetCredentialStore(db)
	checker.SetRateLimitTracker(rateTracker)
	bus := events.New()
	queue := engine.NewQueue(db, bus, log.Logger)
	updater := engine.NewUpdater(client, checker, db, queue, cfg, log, clk, notifier, bus)
	updater.SetSettingsReader(db)
	updater.SetRateLimitTracker(rateTracker)
	scheduler := engine.NewScheduler(updater, cfg, log, clk)
	scheduler.SetSettingsReader(db)
	digestSched := engine.NewDigestScheduler(db, queue, notifier, bus, log, clk)
	digestSched.SetSettingsReader(db)
	// Start web dashboard if enabled.
	if cfg.WebEnabled {
		srv := web.NewServer(web.Dependencies{
			Store:              &storeAdapter{db},
			Queue:              &queueAdapter{queue},
			Docker:             &dockerAdapter{client},
			Updater:            updater,
			Config:             cfg,
			ConfigWriter:       cfg,
			EventBus:           bus,
			Snapshots:          &snapshotAdapter{db},
			Rollback:           &rollbackAdapter{d: client, s: db, log: log},
			Restarter:          &restartAdapter{client},
			Registry:           &registryAdapter{log: log},
			RegistryChecker:    &registryCheckerAdapter{checker: checker},
			Policy:             &policyStoreAdapter{db},
			EventLog:           &eventLogAdapter{db},
			Scheduler:          scheduler,
			SettingsStore:      &settingsStoreAdapter{db},
			Stopper:            &stopAdapter{client},
			Starter:            &startAdapter{client},
			SelfUpdater:        &selfUpdateAdapter{updater: engine.NewSelfUpdater(client, log)},
			NotifyConfig:       &notifyConfigAdapter{db},
			NotifyReconfigurer: notifier,
			NotifyState:        &notifyStateAdapter{db},
			IgnoredVersions:     &ignoredVersionAdapter{db},
			RegistryCredentials: &registryCredentialAdapter{db},
			RateTracker:         &rateLimitAdapter{rateTracker},
			Digest:              digestSched,
			Auth:                authSvc,
			Log:                 log.Logger,
		})

		// Configure WebAuthn passkeys if RPID is set.
		if cfg.WebAuthnEnabled() {
			wa, waErr := webauthn.New(&webauthn.Config{
				RPDisplayName: cfg.WebAuthnDisplayName,
				RPID:          cfg.WebAuthnRPID,
				RPOrigins:     cfg.WebAuthnOriginList(),
			})
			if waErr != nil {
				log.Error("failed to create WebAuthn instance", "error", waErr)
				os.Exit(1)
			}
			srv.SetWebAuthn(wa)
			log.Info("webauthn passkeys enabled", "rpid", cfg.WebAuthnRPID, "origins", cfg.WebAuthnOrigins)
		}

		// Configure TLS if enabled.
		if cfg.TLSCert != "" && cfg.TLSKey != "" {
			srv.SetTLS(cfg.TLSCert, cfg.TLSKey)
			log.Info("TLS enabled (user-provided certificate)")
		} else if cfg.TLSAuto {
			dataDir := filepath.Dir(cfg.DBPath)
			certPath, keyPath, tlsErr := web.EnsureSelfSignedCert(dataDir)
			if tlsErr != nil {
				log.Error("failed to generate self-signed certificate", "error", tlsErr)
				os.Exit(1)
			}
			srv.SetTLS(certPath, keyPath)
			log.Info("TLS enabled (auto-generated self-signed certificate)", "cert", certPath)
		}

		// Set bootstrap token for first-run setup.
		scheme := "http"
		if cfg.TLSEnabled() {
			scheme = "https"
		}
		if bootstrapToken != "" {
			srv.SetBootstrapToken(bootstrapToken)
			fmt.Println("=============================================")
			fmt.Printf("Setup URL: %s://localhost:%s/setup?token=%s\n", scheme, cfg.WebPort, bootstrapToken)
			fmt.Println("=============================================")
		}

		go func() {
			addr := net.JoinHostPort("", cfg.WebPort)
			if err := srv.ListenAndServe(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("web server error", "error", err)
			}
		}()

		go func() {
			<-ctx.Done()
			_ = srv.Shutdown(context.Background())
		}()

		go func() {
			if err := digestSched.Run(ctx); err != nil {
				log.Error("digest scheduler error", "error", err)
			}
		}()

		// Session cleanup goroutine — purge expired sessions hourly.
		go func() {
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					n, cleanErr := authSvc.CleanupExpiredSessions()
					if cleanErr != nil {
						log.Warn("session cleanup failed", "error", cleanErr)
					} else if n > 0 {
						log.Info("cleaned up expired sessions", "count", n)
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	log.Info("sentinel started", "version", version)

	if err := scheduler.Run(ctx); err != nil {
		log.Error("sentinel exited with error", "error", err)
		os.Exit(1)
	}

	log.Info("sentinel shutdown complete")
}

// parseHeaders parses comma-separated "Key:Value" pairs into a map.
func parseHeaders(s string) map[string]string {
	if s == "" {
		return nil
	}
	headers := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		kv := strings.SplitN(strings.TrimSpace(pair), ":", 2)
		if len(kv) == 2 {
			headers[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return headers
}

// --- Adapters bridging concrete types to web.Dependencies interfaces ---

// storeAdapter converts store.Store to web.HistoryStore.
type storeAdapter struct{ s *store.Store }

func (a *storeAdapter) ListHistory(limit int) ([]web.UpdateRecord, error) {
	records, err := a.s.ListHistory(limit)
	if err != nil {
		return nil, err
	}
	result := make([]web.UpdateRecord, len(records))
	for i, r := range records {
		result[i] = web.UpdateRecord{
			Timestamp:     r.Timestamp,
			ContainerName: r.ContainerName,
			OldImage:      r.OldImage,
			OldDigest:     r.OldDigest,
			NewImage:      r.NewImage,
			NewDigest:     r.NewDigest,
			Outcome:       r.Outcome,
			Duration:      r.Duration,
			Error:         r.Error,
		}
	}
	return result, nil
}

func (a *storeAdapter) ListHistoryByContainer(name string, limit int) ([]web.UpdateRecord, error) {
	records, err := a.s.ListHistoryByContainer(name, limit)
	if err != nil {
		return nil, err
	}
	result := make([]web.UpdateRecord, len(records))
	for i, r := range records {
		result[i] = web.UpdateRecord{
			Timestamp:     r.Timestamp,
			ContainerName: r.ContainerName,
			OldImage:      r.OldImage,
			OldDigest:     r.OldDigest,
			NewImage:      r.NewImage,
			NewDigest:     r.NewDigest,
			Outcome:       r.Outcome,
			Duration:      r.Duration,
			Error:         r.Error,
		}
	}
	return result, nil
}

func (a *storeAdapter) GetMaintenance(name string) (bool, error) {
	return a.s.GetMaintenance(name)
}

// snapshotAdapter converts store.Store to web.SnapshotStore.
type snapshotAdapter struct{ s *store.Store }

func (a *snapshotAdapter) ListSnapshots(name string) ([]web.SnapshotEntry, error) {
	entries, err := a.s.ListSnapshots(name)
	if err != nil {
		return nil, err
	}
	result := make([]web.SnapshotEntry, len(entries))
	for i, e := range entries {
		// Extract image reference from the snapshot JSON data.
		imageRef := extractImageFromSnapshot(e.Data)
		result[i] = web.SnapshotEntry{
			Timestamp: e.Timestamp,
			ImageRef:  imageRef,
		}
	}
	return result, nil
}

// extractImageFromSnapshot parses the image reference from a container inspect JSON snapshot.
func extractImageFromSnapshot(data []byte) string {
	var snap struct {
		Config *struct {
			Image string `json:"Image"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		return ""
	}
	if snap.Config != nil {
		return snap.Config.Image
	}
	return ""
}

// queueAdapter converts engine.Queue to web.UpdateQueue.
type queueAdapter struct{ q *engine.Queue }

func (a *queueAdapter) List() []web.PendingUpdate {
	items := a.q.List()
	result := make([]web.PendingUpdate, len(items))
	for i, item := range items {
		result[i] = convertPendingUpdate(item)
	}
	return result
}

func (a *queueAdapter) Add(update web.PendingUpdate) {
	a.q.Add(engine.PendingUpdate{
		ContainerID:            update.ContainerID,
		ContainerName:          update.ContainerName,
		CurrentImage:           update.CurrentImage,
		CurrentDigest:          update.CurrentDigest,
		RemoteDigest:           update.RemoteDigest,
		DetectedAt:             update.DetectedAt,
		NewerVersions:          update.NewerVersions,
		ResolvedCurrentVersion: update.ResolvedCurrentVersion,
		ResolvedTargetVersion:  update.ResolvedTargetVersion,
	})
}

func (a *queueAdapter) Get(name string) (web.PendingUpdate, bool) {
	item, ok := a.q.Get(name)
	if !ok {
		return web.PendingUpdate{}, false
	}
	return convertPendingUpdate(item), true
}

func (a *queueAdapter) Approve(name string) (web.PendingUpdate, bool) {
	item, ok := a.q.Approve(name)
	if !ok {
		return web.PendingUpdate{}, false
	}
	return convertPendingUpdate(item), true
}

func (a *queueAdapter) Remove(name string) { a.q.Remove(name) }

func convertPendingUpdate(item engine.PendingUpdate) web.PendingUpdate {
	return web.PendingUpdate{
		ContainerID:            item.ContainerID,
		ContainerName:          item.ContainerName,
		CurrentImage:           item.CurrentImage,
		CurrentDigest:          item.CurrentDigest,
		RemoteDigest:           item.RemoteDigest,
		DetectedAt:             item.DetectedAt,
		NewerVersions:          item.NewerVersions,
		ResolvedCurrentVersion: item.ResolvedCurrentVersion,
		ResolvedTargetVersion:  item.ResolvedTargetVersion,
	}
}

// dockerAdapter converts docker.Client to web.ContainerLister.
type dockerAdapter struct{ c *docker.Client }

func (a *dockerAdapter) ListContainers(ctx context.Context) ([]web.ContainerSummary, error) {
	containers, err := a.c.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]web.ContainerSummary, len(containers))
	for i, c := range containers {
		result[i] = web.ContainerSummary{
			ID:     c.ID,
			Names:  c.Names,
			Image:  c.Image,
			Labels: c.Labels,
			State:  string(c.State),
		}
	}
	return result, nil
}

func (a *dockerAdapter) ListAllContainers(ctx context.Context) ([]web.ContainerSummary, error) {
	containers, err := a.c.ListAllContainers(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]web.ContainerSummary, len(containers))
	for i, c := range containers {
		result[i] = web.ContainerSummary{
			ID:     c.ID,
			Names:  c.Names,
			Image:  c.Image,
			Labels: c.Labels,
			State:  string(c.State),
		}
	}
	return result, nil
}

func (a *dockerAdapter) InspectContainer(ctx context.Context, id string) (web.ContainerInspect, error) {
	inspect, err := a.c.InspectContainer(ctx, id)
	if err != nil {
		return web.ContainerInspect{}, err
	}
	var ci web.ContainerInspect
	ci.ID = inspect.ID
	ci.Name = inspect.Name
	if inspect.Config != nil {
		ci.Image = inspect.Config.Image
	}
	if inspect.State != nil {
		ci.State.Status = string(inspect.State.Status)
		ci.State.Running = inspect.State.Running
		ci.State.Restarting = inspect.State.Restarting
	}
	return ci, nil
}

// restartAdapter bridges docker.Client to web.ContainerRestarter.
type restartAdapter struct{ c *docker.Client }

func (a *restartAdapter) RestartContainer(ctx context.Context, id string) error {
	return a.c.RestartContainer(ctx, id)
}

// rollbackAdapter bridges engine.RollbackFromStore to web.ContainerRollback.
type rollbackAdapter struct {
	d   *docker.Client
	s   *store.Store
	log *logging.Logger
}

func (a *rollbackAdapter) RollbackContainer(ctx context.Context, name string) error {
	return engine.RollbackFromStore(ctx, a.d, a.s, name, a.log)
}

// registryAdapter bridges registry.ListTags to web.RegistryVersionChecker.
type registryAdapter struct {
	log *logging.Logger
}

func (a *registryAdapter) ListVersions(ctx context.Context, imageRef string) ([]string, error) {
	tag := registry.ExtractTag(imageRef)
	if tag == "" {
		return nil, nil
	}
	repo := registry.NormaliseRepo(imageRef)
	token, err := registry.FetchAnonymousToken(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("fetch token: %w", err)
	}
	tagsResult, err := registry.ListTags(ctx, imageRef, token, "docker.io", nil)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	// Filter to semver-parseable tags and return newest first.
	newer := registry.NewerVersions(tag, tagsResult.Tags)
	versions := make([]string, len(newer))
	for i, sv := range newer {
		versions[i] = sv.Raw
	}
	return versions, nil
}

// registryCheckerAdapter bridges registry.Checker to web.RegistryChecker.
type registryCheckerAdapter struct {
	checker *registry.Checker
}

func (a *registryCheckerAdapter) CheckForUpdate(ctx context.Context, imageRef string) (bool, []string, string, string, error) {
	result := a.checker.CheckVersioned(ctx, imageRef)
	if result.Error != nil {
		return false, nil, "", "", result.Error
	}
	if result.IsLocal {
		return false, nil, "", "", nil
	}
	return result.UpdateAvailable, result.NewerVersions, result.ResolvedCurrentVersion, result.ResolvedTargetVersion, nil
}

// policyStoreAdapter bridges store.Store to web.PolicyStore.
type policyStoreAdapter struct{ s *store.Store }

func (a *policyStoreAdapter) GetPolicyOverride(name string) (string, bool) {
	return a.s.GetPolicyOverride(name)
}

func (a *policyStoreAdapter) SetPolicyOverride(name, policy string) error {
	return a.s.SetPolicyOverride(name, policy)
}

func (a *policyStoreAdapter) DeletePolicyOverride(name string) error {
	return a.s.DeletePolicyOverride(name)
}

func (a *policyStoreAdapter) AllPolicyOverrides() map[string]string {
	return a.s.AllPolicyOverrides()
}

// eventLogAdapter bridges store.Store to web.EventLogger.
type eventLogAdapter struct{ s *store.Store }

func (a *eventLogAdapter) AppendLog(entry web.LogEntry) error {
	return a.s.AppendLog(store.LogEntry{
		Timestamp: entry.Timestamp,
		Type:      entry.Type,
		Message:   entry.Message,
		Container: entry.Container,
	})
}

func (a *eventLogAdapter) ListLogs(limit int) ([]web.LogEntry, error) {
	entries, err := a.s.ListLogs(limit)
	if err != nil {
		return nil, err
	}
	result := make([]web.LogEntry, len(entries))
	for i, e := range entries {
		result[i] = web.LogEntry{
			Timestamp: e.Timestamp,
			Type:      e.Type,
			Message:   e.Message,
			Container: e.Container,
		}
	}
	return result, nil
}

// settingsStoreAdapter bridges store.Store to web.SettingsStore.
type settingsStoreAdapter struct{ s *store.Store }

func (a *settingsStoreAdapter) SaveSetting(key, value string) error {
	return a.s.SaveSetting(key, value)
}

func (a *settingsStoreAdapter) LoadSetting(key string) (string, error) {
	return a.s.LoadSetting(key)
}

func (a *settingsStoreAdapter) GetAllSettings() (map[string]string, error) {
	return a.s.GetAllSettings()
}

// selfUpdateAdapter bridges engine.SelfUpdater to web.SelfUpdater.
type selfUpdateAdapter struct {
	updater *engine.SelfUpdater
}

func (a *selfUpdateAdapter) Update(ctx context.Context) error {
	return a.updater.Update(ctx)
}

// stopAdapter bridges docker.Client to web.ContainerStopper.
type stopAdapter struct{ c *docker.Client }

func (a *stopAdapter) StopContainer(ctx context.Context, id string) error {
	return a.c.StopContainer(ctx, id, 10)
}

// startAdapter bridges docker.Client to web.ContainerStarter.
type startAdapter struct{ c *docker.Client }

func (a *startAdapter) StartContainer(ctx context.Context, id string) error {
	return a.c.StartContainer(ctx, id)
}

// notifyConfigAdapter bridges store.Store to web.NotificationConfigStore.
type notifyConfigAdapter struct{ s *store.Store }

func (a *notifyConfigAdapter) GetNotificationChannels() ([]notify.Channel, error) {
	return a.s.GetNotificationChannels()
}

func (a *notifyConfigAdapter) SetNotificationChannels(channels []notify.Channel) error {
	return a.s.SetNotificationChannels(channels)
}

// notifyStateAdapter bridges store.Store to web.NotifyStateStore.
type notifyStateAdapter struct{ s *store.Store }

func (a *notifyStateAdapter) GetNotifyPref(name string) (*web.NotifyPref, error) {
	p, err := a.s.GetNotifyPref(name)
	if err != nil || p == nil {
		return nil, err
	}
	return &web.NotifyPref{Mode: p.Mode}, nil
}

func (a *notifyStateAdapter) SetNotifyPref(name string, pref *web.NotifyPref) error {
	return a.s.SetNotifyPref(name, &store.NotifyPref{Mode: pref.Mode})
}

func (a *notifyStateAdapter) DeleteNotifyPref(name string) error {
	return a.s.DeleteNotifyPref(name)
}

func (a *notifyStateAdapter) AllNotifyPrefs() (map[string]*web.NotifyPref, error) {
	prefs, err := a.s.AllNotifyPrefs()
	if err != nil {
		return nil, err
	}
	result := make(map[string]*web.NotifyPref, len(prefs))
	for k, v := range prefs {
		result[k] = &web.NotifyPref{Mode: v.Mode}
	}
	return result, nil
}

func (a *notifyStateAdapter) AllNotifyStates() (map[string]*web.NotifyState, error) {
	states, err := a.s.AllNotifyStates()
	if err != nil {
		return nil, err
	}
	result := make(map[string]*web.NotifyState, len(states))
	for k, v := range states {
		result[k] = &web.NotifyState{
			LastDigest:   v.LastDigest,
			LastNotified: v.LastNotified,
			FirstSeen:    v.FirstSeen,
		}
	}
	return result, nil
}

func (a *notifyStateAdapter) ClearNotifyState(name string) error {
	return a.s.ClearNotifyState(name)
}

// ignoredVersionAdapter bridges store.Store to web.IgnoredVersionStore.
type ignoredVersionAdapter struct{ s *store.Store }

func (a *ignoredVersionAdapter) AddIgnoredVersion(containerName, version string) error {
	return a.s.AddIgnoredVersion(containerName, version)
}

func (a *ignoredVersionAdapter) GetIgnoredVersions(containerName string) ([]string, error) {
	return a.s.GetIgnoredVersions(containerName)
}

func (a *ignoredVersionAdapter) ClearIgnoredVersions(containerName string) error {
	return a.s.ClearIgnoredVersions(containerName)
}

// registryCredentialAdapter bridges store.Store (which returns registry.RegistryCredential)
// to web.RegistryCredentialStore (which uses web.RegistryCredential).
type registryCredentialAdapter struct{ s *store.Store }

func (a *registryCredentialAdapter) GetRegistryCredentials() ([]web.RegistryCredential, error) {
	creds, err := a.s.GetRegistryCredentials()
	if err != nil {
		return nil, err
	}
	result := make([]web.RegistryCredential, len(creds))
	for i, c := range creds {
		result[i] = web.RegistryCredential{
			ID:       c.ID,
			Registry: c.Registry,
			Username: c.Username,
			Secret:   c.Secret,
		}
	}
	return result, nil
}

func (a *registryCredentialAdapter) SetRegistryCredentials(creds []web.RegistryCredential) error {
	regCreds := make([]registry.RegistryCredential, len(creds))
	for i, c := range creds {
		regCreds[i] = registry.RegistryCredential{
			ID:       c.ID,
			Registry: c.Registry,
			Username: c.Username,
			Secret:   c.Secret,
		}
	}
	return a.s.SetRegistryCredentials(regCreds)
}

// rateLimitAdapter bridges registry.RateLimitTracker to web.RateLimitProvider.
type rateLimitAdapter struct{ t *registry.RateLimitTracker }

func (a *rateLimitAdapter) Status() []web.RateLimitStatus {
	statuses := a.t.Status()
	result := make([]web.RateLimitStatus, len(statuses))
	for i, s := range statuses {
		result[i] = web.RateLimitStatus{
			Registry:       s.Registry,
			Limit:          s.Limit,
			Remaining:      s.Remaining,
			ResetAt:        s.ResetAt,
			IsAuth:         s.IsAuth,
			HasLimits:      s.HasLimits,
			ContainerCount: s.ContainerCount,
			LastUpdated:    s.LastUpdated,
		}
	}
	return result
}

func (a *rateLimitAdapter) OverallHealth() string {
	return a.t.OverallHealth()
}
