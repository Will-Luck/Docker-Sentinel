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
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/agent"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	_ "github.com/Will-Luck/Docker-Sentinel/internal/metrics"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/web"
)

// version and commit are set at build time via ldflags:
//
//	-X main.version=$(VERSION) -X main.commit=$(COMMIT)
//
// version defaults to "dev" for untagged local builds.
// commit defaults to "unknown" when git info isn't available (e.g. Docker build
// without --build-arg COMMIT=...).
var version = "dev"
var commit = "unknown"

// versionString returns the formatted version for display, including the
// short commit hash in parentheses when available.
// Examples: "v2.0.1 (abc1234)", "dev (abc1234)", "dev" (no git info).
func versionString() string {
	if commit != "" && commit != "unknown" {
		return version + " (" + commit + ")"
	}
	return version
}

func main() {
	// Subcommand dispatch: "sentinel server" or "sentinel agent".
	// Bare "sentinel" defaults to server mode for backwards compatibility.
	mode := ""
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "server":
			mode = "server"
			os.Args = append(os.Args[:1], os.Args[2:]...) // strip subcommand
		case "agent":
			mode = "agent"
			os.Args = append(os.Args[:1], os.Args[2:]...) // strip subcommand
		}
	}

	cfg := config.Load()

	// Subcommand takes precedence over SENTINEL_MODE env var.
	if mode != "" {
		cfg.Mode = mode
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(1)
	}

	// Auto-detect CookieSecure when not explicitly set: secure only if TLS is enabled.
	if os.Getenv("SENTINEL_COOKIE_SECURE") == "" {
		cfg.CookieSecure = cfg.TLSEnabled()
	}
	log := logging.New(cfg.LogJSON)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	fmt.Println("Docker-Sentinel " + versionString())
	if cfg.Mode != "" {
		fmt.Printf("Mode: %s\n", cfg.Mode)
	}
	fmt.Println("=============================================")

	// Agent mode runs a completely different code path.
	if cfg.IsAgent() {
		runAgent(ctx, cfg, log)
		return
	}

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

	// Check if instance has been configured via the wizard.
	instanceRole, _ := db.LoadSetting("instance_role")
	needsWizard := instanceRole == ""

	// Env var overrides: if SENTINEL_MODE is explicitly set AND auth_setup_complete is true,
	// skip wizard (backwards compat for existing deployments).
	if cfg.Mode != "" {
		if v, _ := db.LoadSetting("auth_setup_complete"); v == "true" {
			needsWizard = false
			if instanceRole == "" {
				_ = db.SaveSetting("instance_role", cfg.Mode)
				instanceRole = cfg.Mode
			}
		}
	}

	if needsWizard {
		runWizard(ctx, cfg, db, authSvc, log)
		// Re-read role from DB after wizard completes.
		instanceRole, _ = db.LoadSetting("instance_role")
		if instanceRole == "" {
			// Wizard was cancelled or timed out — exit cleanly.
			log.Info("wizard did not complete, exiting")
			return
		}
		cfg.Mode = instanceRole
	}

	// If instance_role is now "agent", hand off to agent mode.
	if instanceRole == "agent" {
		// Load agent settings from DB if not set via env.
		if cfg.ServerAddr == "" {
			cfg.ServerAddr, _ = db.LoadSetting("server_addr")
		}
		if cfg.EnrollToken == "" {
			cfg.EnrollToken, _ = db.LoadSetting("enroll_token")
		}
		if cfg.HostName == "" {
			cfg.HostName, _ = db.LoadSetting("host_name")
		}
		cfg.Mode = "agent"
		runAgent(ctx, cfg, log)
		return
	}

	// Set up a 5-minute setup window if no admin user exists yet.
	var setupDeadline time.Time
	if authSvc.NeedsSetup() {
		setupDeadline = time.Now().Add(5 * time.Minute)
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

	if saved, err := db.LoadSetting("latest_auto_update"); err == nil && saved != "" {
		cfg.SetLatestAutoUpdate(saved == "true")
		log.Info("loaded persisted latest auto-update setting", "enabled", saved == "true")
	}
	if saved, err := db.LoadSetting("image_cleanup"); err == nil && saved != "" {
		cfg.SetImageCleanup(saved == "true")
		log.Info("loaded persisted image cleanup setting", "enabled", saved == "true")
	}
	if saved, err := db.LoadSetting("schedule"); err == nil && saved != "" {
		cfg.SetSchedule(saved)
		log.Info("loaded persisted schedule", "schedule", saved)
	}
	if saved, err := db.LoadSetting("hooks_enabled"); err == nil && saved != "" {
		cfg.SetHooksEnabled(saved == "true")
		log.Info("loaded persisted hooks enabled setting", "enabled", saved == "true")
	}
	if saved, err := db.LoadSetting("hooks_write_labels"); err == nil && saved != "" {
		cfg.SetHooksWriteLabels(saved == "true")
		log.Info("loaded persisted hooks write labels setting", "enabled", saved == "true")
	}
	if saved, err := db.LoadSetting("dependency_aware"); err == nil && saved != "" {
		cfg.SetDependencyAware(saved == "true")
		log.Info("loaded persisted dependency-aware setting", "enabled", saved == "true")
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
	if rlData, rlErr := db.LoadRateLimits(); rlErr == nil && rlData != nil {
		if importErr := rateTracker.Import(rlData); importErr != nil {
			log.Warn("failed to load persisted rate limits", "error", importErr)
		} else {
			log.Info("loaded persisted rate limits")
		}
	}
	ghcrCache := registry.NewGHCRCache(24 * time.Hour)
	if ghcrData, ghcrErr := db.LoadGHCRCache(); ghcrErr == nil && ghcrData != nil {
		if importErr := ghcrCache.Import(ghcrData); importErr != nil {
			log.Warn("failed to load persisted GHCR cache", "error", importErr)
		} else {
			log.Info("loaded persisted GHCR cache")
		}
	}
	checker.SetCredentialStore(db)
	checker.SetRateLimitTracker(rateTracker)
	bus := events.New()
	queue := engine.NewQueue(db, bus, log.Logger)
	updater := engine.NewUpdater(client, checker, db, queue, cfg, log, clk, notifier, bus)
	updater.SetSettingsReader(db)
	updater.SetRateLimitTracker(rateTracker)
	updater.SetRateLimitSaver(db.SaveRateLimits)
	updater.SetGHCRCache(ghcrCache)
	updater.SetGHCRSaver(db.SaveGHCRCache)

	// Create hook runner if hooks are enabled.
	hookRunner := hooks.NewRunner(client, &hookStoreAdapter{db}, log.Logger)
	updater.SetHookRunner(hookRunner)

	// Detect Swarm mode.
	isSwarm := client.IsSwarmManager(ctx)
	if isSwarm {
		log.Info("swarm mode detected — service monitoring enabled")
	}

	scheduler := engine.NewScheduler(updater, cfg, log, clk)
	scheduler.SetSettingsReader(db)
	digestSched := engine.NewDigestScheduler(db, queue, notifier, bus, log, clk)
	digestSched.SetSettingsReader(db)

	// Cluster lifecycle — centralised start/stop via clusterManager.
	clusterCtrl := web.NewClusterController()
	cm := &clusterManager{
		db:      db,
		bus:     bus,
		log:     log.Logger,
		updater: updater,
		ctrl:    clusterCtrl,
		dataDir: cfg.ClusterDataDir,
	}

	// Env var takes precedence; fall back to DB setting.
	clusterEnabled := cfg.ClusterEnabled
	if !clusterEnabled {
		if v, _ := db.LoadSetting(store.SettingClusterEnabled); v == "true" {
			clusterEnabled = true
		}
	}

	if clusterEnabled {
		if err := cm.Start(); err != nil {
			log.Error("failed to start cluster", "error", err)
			os.Exit(1)
		}
		defer cm.Stop()
	}

	if cfg.WebEnabled {
		webDeps := web.Dependencies{
			Store:               &storeAdapter{db},
			AboutStore:          &aboutStoreAdapter{db},
			Queue:               &queueAdapter{queue},
			Docker:              &dockerAdapter{client},
			Updater:             updater,
			Config:              cfg,
			ConfigWriter:        cfg,
			EventBus:            bus,
			Snapshots:           &snapshotAdapter{db},
			Rollback:            &rollbackAdapter{d: client, s: db, log: log},
			Restarter:           &restartAdapter{client},
			Registry:            &registryAdapter{log: log},
			RegistryChecker:     &registryCheckerAdapter{checker: checker},
			Policy:              &policyStoreAdapter{db},
			EventLog:            &eventLogAdapter{db},
			Scheduler:           scheduler,
			SettingsStore:       &settingsStoreAdapter{db},
			Stopper:             &stopAdapter{client},
			Starter:             &startAdapter{client},
			SelfUpdater:         &selfUpdateAdapter{updater: engine.NewSelfUpdater(client, log)},
			NotifyConfig:        &notifyConfigAdapter{db},
			NotifyReconfigurer:  notifier,
			NotifyState:         &notifyStateAdapter{db},
			IgnoredVersions:     &ignoredVersionAdapter{db},
			RegistryCredentials: &registryCredentialAdapter{db},
			RateTracker:         &rateLimitAdapter{t: rateTracker, saver: db.SaveRateLimits},
			GHCRCache:           &ghcrCacheAdapter{c: ghcrCache},
			HookStore:           &webHookStoreAdapter{db},
			Cluster:             clusterCtrl,
			MetricsEnabled:      cfg.MetricsEnabled,
			Digest:              digestSched,
			Auth:                authSvc,
			Version:             versionString(),
			Commit:              commit,
			Log:                 log.Logger,
		}
		if isSwarm {
			webDeps.Swarm = &swarmAdapter{client: client, updater: updater}
		}
		srv := web.NewServer(webDeps)
		srv.SetClusterLifecycle(cm)

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
		if !setupDeadline.IsZero() {
			srv.SetSetupDeadline(setupDeadline)
			fmt.Println("=============================================")
			fmt.Println("First-run setup required!")
			fmt.Println("")
			fmt.Printf("  Open %s://<your-host>:%s/setup\n", scheme, cfg.WebPort)
			fmt.Println("  to create your admin account.")
			fmt.Println("")
			fmt.Println("  This page will be available for 5 minutes.")
			fmt.Println("  Restart the container to get a new window.")
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

	log.Info("sentinel started", "version", version, "commit", commit)

	if err := scheduler.Run(ctx); err != nil {
		log.Error("sentinel exited with error", "error", err)
		os.Exit(1)
	}

	log.Info("sentinel shutdown complete")
}

// runAgent starts Sentinel in agent mode. This is a completely separate
// code path from the server — it connects to a remote Sentinel server
// over gRPC and executes update commands on the local Docker host.
func runAgent(ctx context.Context, cfg *config.Config, log *logging.Logger) {
	fmt.Println("Docker-Sentinel Agent " + versionString())
	fmt.Println("=============================================")
	fmt.Printf("SENTINEL_SERVER_ADDR=%s\n", cfg.ServerAddr)
	fmt.Printf("SENTINEL_HOST_NAME=%s\n", cfg.HostName)
	fmt.Printf("SENTINEL_CLUSTER_DIR=%s\n", cfg.ClusterDataDir)

	client, err := docker.NewClient(cfg.DockerSock)
	if err != nil {
		log.Error("failed to create Docker client", "error", err)
		os.Exit(1)
	}
	defer client.Close()

	agentCfg := agent.Config{
		ServerAddr:         cfg.ServerAddr,
		EnrollToken:        cfg.EnrollToken,
		HostName:           cfg.HostName,
		DataDir:            cfg.ClusterDataDir,
		GracePeriodOffline: cfg.GracePeriodOffline,
		DockerSock:         cfg.DockerSock,
		Version:            versionString(),
	}

	a := agent.New(agentCfg, client, log.Logger)

	log.Info("starting agent mode", "server", cfg.ServerAddr, "host", cfg.HostName)
	if err := a.Run(ctx); err != nil {
		log.Error("agent exited with error", "error", err)
		os.Exit(1)
	}
	log.Info("agent shutdown complete")
}

// runWizard starts the setup wizard server and blocks until setup completes
// or ctx is cancelled.
func runWizard(ctx context.Context, cfg *config.Config, db *store.Store, authSvc *auth.Service, log *logging.Logger) {
	fmt.Println("=============================================")
	fmt.Println("First-run setup required!")
	fmt.Println("")
	fmt.Printf("  Open http://<your-host>:%s/setup\n", cfg.WebPort)
	fmt.Println("  to configure this instance.")
	fmt.Println("")
	fmt.Println("  This page will be available for 5 minutes.")
	fmt.Println("  Restart the container to get a new window.")
	fmt.Println("=============================================")

	ws := web.NewWizardServer(web.WizardDeps{
		SettingsStore: &settingsStoreAdapter{db},
		Auth:          authSvc,
		Log:           log.Logger,
		Version:       versionString(),
		ClusterPort:   cfg.ClusterPort,
	})

	addr := net.JoinHostPort("", cfg.WebPort)
	go func() {
		if err := ws.ListenAndServe(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("wizard server error", "error", err)
		}
	}()

	select {
	case <-ws.Done():
		log.Info("wizard setup complete")
	case <-ctx.Done():
		log.Info("wizard cancelled by signal")
	}

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = ws.Shutdown(shutCtx)
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
