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
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/backup"
	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/metrics"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/npm"
	portainerpkg "github.com/Will-Luck/Docker-Sentinel/internal/portainer"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/scanner"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
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

	// Open DB first so we can load TLS settings before creating the Docker client.
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	var dbClosed sync.Once
	defer dbClosed.Do(func() { db.Close() })

	// Load Docker TLS certificate paths from BoltDB.
	var tlsCfg *docker.TLSConfig
	tlsCA, _ := db.LoadSetting(store.SettingDockerTLSCA)
	tlsCert, _ := db.LoadSetting(store.SettingDockerTLSCert)
	tlsKey, _ := db.LoadSetting(store.SettingDockerTLSKey)
	if tlsCA != "" && tlsCert != "" && tlsKey != "" {
		tlsCfg = &docker.TLSConfig{CACert: tlsCA, ClientCert: tlsCert, ClientKey: tlsKey}
		log.Info("Docker TLS configured", "ca", tlsCA, "cert", tlsCert)
	}

	client, err := docker.NewClient(cfg.DockerSock, tlsCfg)
	if err != nil {
		log.Error("failed to create Docker client", "error", err)
		os.Exit(1)
	}
	var clientClosed sync.Once
	defer clientClosed.Do(func() { client.Close() })

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
		PendingTOTP:    db,
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

	// If users already exist but instance_role is empty (upgrade from older version),
	// default to server mode and skip the wizard.
	if needsWizard {
		if n, _ := db.UserCount(); n > 0 {
			needsWizard = false
			instanceRole = "server"
			_ = db.SaveSetting("instance_role", "server")
			_ = db.SaveSetting("auth_setup_complete", "true")
			log.Info("existing users found, defaulting to server mode")
		}
	}

	// Auto-enrollment: if SENTINEL_ENROLL_TOKEN is set on a fresh container, skip wizard.
	if needsWizard && cfg.EnrollToken != "" && cfg.ServerAddr != "" {
		log.Info("auto-enrolling as agent", "server", cfg.ServerAddr)
		_ = db.SaveSetting("instance_role", "agent")
		_ = db.SaveSetting("server_addr", cfg.ServerAddr)
		_ = db.SaveSetting("auth_setup_complete", "true")
		if cfg.HostName != "" {
			_ = db.SaveSetting("host_name", cfg.HostName)
		}

		// Generate random admin credentials for the agent mini-UI.
		randomPass := generateRandomPassword()
		hash, err := auth.HashPassword(randomPass)
		if err != nil {
			log.Error("auto-enroll: failed to hash password", "error", err)
			os.Exit(1)
		}
		userID, err := auth.GenerateUserID()
		if err != nil {
			log.Error("auto-enroll: failed to generate user ID", "error", err)
			os.Exit(1)
		}
		if err := db.CreateFirstUser(auth.User{
			ID:           userID,
			Username:     "admin",
			PasswordHash: hash,
			RoleID:       auth.RoleAdminID,
			CreatedAt:    time.Now().UTC(),
			UpdatedAt:    time.Now().UTC(),
		}); err != nil {
			log.Error("auto-enroll: failed to create admin user", "error", err)
			os.Exit(1)
		}

		// Write credentials to a file instead of stdout to avoid leaking
		// secrets in container logs (issue #43).
		credFile := filepath.Join(filepath.Dir(cfg.DBPath), "sentinel-credentials.txt")
		credContent := fmt.Sprintf("Agent UI login: admin / %s\nChange this password after first login.\n", randomPass)
		if writeErr := os.WriteFile(credFile, []byte(credContent), 0600); writeErr != nil {
			log.Error("failed to write credentials file", "error", writeErr)
			os.Exit(1)
		}
		fmt.Println("=============================================")
		fmt.Println("Auto-enrolled as agent.")
		fmt.Printf("Credentials written to: %s\n", credFile)
		fmt.Println("=============================================")

		cfg.Mode = "agent"
		needsWizard = false
		instanceRole = "agent"
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
		// Close DB and Docker client before runAgent opens its own handles.
		// BoltDB uses file-level locking — two open handles deadlock.
		db.Close()
		client.Close()
		runAgent(ctx, cfg, log)
		return
	}

	// Set up a 5-minute setup window if no admin user exists yet.
	var setupDeadline time.Time
	freshSetup := needsWizard // wizard just ran — user hasn't seen the dashboard yet
	if authSvc.NeedsSetup() {
		setupDeadline = time.Now().Add(5 * time.Minute)
		freshSetup = true
	}

	// Gate the initial scan: on fresh setup, wait until the user loads the
	// dashboard so the scanner doesn't run wild before they've seen the UI.
	scanGate := make(chan struct{})
	if !freshSetup {
		close(scanGate)
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

	// Load notification batch window from settings (default: 0 = disabled).
	if bw := loadSettingStr(db, "notification_batch_window"); bw != "" && bw != "0" {
		if d, err := time.ParseDuration(bw); err == nil && d > 0 {
			notifier.SetBatchWindow(d)
			log.Info("notification batching enabled", "window", d.String())
		}
	}

	// Load notification retry settings (default: disabled).
	if countStr, _ := db.LoadSetting(store.SettingNotifyRetryCount); countStr != "" {
		count, _ := strconv.Atoi(countStr)
		backoffStr, _ := db.LoadSetting(store.SettingNotifyRetryBackoff)
		backoff, parseErr := time.ParseDuration(backoffStr)
		if parseErr != nil {
			backoff = 2 * time.Second
		}
		notifier.SetRetry(count, backoff)
		if count > 0 {
			log.Info("notification retry enabled", "count", count, "backoff", backoff.String())
		}
	}

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
	checker.SetDigestEquivalenceChecker(db)
	if vs := db.VersionScope(); vs != "default" {
		checker.SetDefaultScope(docker.ScopeStrict)
	}
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

	// Initialise vulnerability scanner (Trivy) if configured.
	{
		trivyPath := loadSettingStr(db, store.SettingTrivyPath)
		if trivyPath == "" {
			trivyPath = "trivy"
		}
		var scanOpts []scanner.Option
		scanOpts = append(scanOpts, scanner.WithTrivyPath(trivyPath))
		imgScanner := scanner.New(log, scanOpts...)
		if imgScanner.Available() {
			updater.SetScanner(imgScanner)
			log.Info("vulnerability scanner available", "path", trivyPath)
		} else {
			log.Info("trivy not found, vulnerability scanning disabled")
		}
		scanMode := scanner.ParseScanMode(loadSettingStr(db, store.SettingScannerMode))
		updater.SetScanMode(scanMode)
		thresh := loadSettingStr(db, store.SettingScannerThreshold)
		if thresh == "" {
			thresh = string(scanner.SeverityHigh)
		}
		updater.SetSeverityThreshold(scanner.Severity(thresh))
	}

	// Initialise signature verifier (cosign) if configured.
	{
		cosignPath := loadSettingStr(db, store.SettingCosignPath)
		if cosignPath == "" {
			cosignPath = "cosign"
		}
		var verifyOpts []verify.Option
		verifyOpts = append(verifyOpts, verify.WithCosignPath(cosignPath))
		if loadSettingStr(db, store.SettingCosignKeyless) == "true" {
			verifyOpts = append(verifyOpts, verify.WithKeyless())
		}
		if kp := loadSettingStr(db, store.SettingCosignKeyPath); kp != "" {
			verifyOpts = append(verifyOpts, verify.WithKeyPath(kp))
		}
		imgVerifier := verify.New(log, verifyOpts...)
		if imgVerifier.Available() {
			updater.SetVerifier(imgVerifier)
			log.Info("signature verifier available", "path", cosignPath)
		} else {
			log.Info("cosign not found, signature verification disabled")
		}
		verifyMode := verify.ParseMode(loadSettingStr(db, store.SettingVerifyMode))
		updater.SetVerifyMode(verifyMode)
	}

	// Set up HA discovery if enabled and an MQTT channel is configured.
	if haEnabled, _ := db.LoadSetting("ha_discovery_enabled"); haEnabled == "true" {
		if haChannels, haErr := db.GetNotificationChannels(); haErr == nil {
			for _, ch := range haChannels {
				if ch.Type == notify.ProviderMQTT && ch.Enabled {
					var mqttSettings notify.MQTTSettings
					if json.Unmarshal(ch.Settings, &mqttSettings) == nil {
						haPrefix, _ := db.LoadSetting("ha_discovery_prefix")
						clientID := mqttSettings.ClientID
						if clientID == "" {
							clientID = "docker-sentinel"
						}
						ha, haConnErr := notify.NewHADiscovery(notify.HADiscoveryConfig{
							Broker:   mqttSettings.Broker,
							ClientID: clientID,
							Username: mqttSettings.Username,
							Password: mqttSettings.Password,
							Prefix:   haPrefix,
						})
						if haConnErr != nil {
							log.Warn("failed to start HA discovery", "error", haConnErr)
						} else {
							updater.SetHADiscovery(ha)
							defer ha.Close()
							log.Info("home assistant MQTT discovery enabled", "broker", mqttSettings.Broker)
						}
					}
					break
				}
			}
		}
	}

	// Collect local Docker Engine ID for source deduplication.
	// If two sources (local + Portainer + cluster agent) point at the same
	// daemon, the overlap checker uses this ID to auto-block duplicates.
	if eid, err := client.EngineID(ctx); err != nil {
		log.Warn("failed to get local Docker Engine ID", "error", err)
	} else {
		_ = db.SaveSetting("local_engine_id", eid)
		log.Info("local Docker Engine ID", "engine_id", eid)
	}

	// Detect Swarm mode.
	isSwarm := client.IsSwarmManager(ctx)
	if isSwarm {
		log.Info("swarm mode detected — service monitoring enabled")
	}

	selfUpdater := engine.NewSelfUpdater(client, log)
	scheduler := engine.NewScheduler(updater, cfg, log, clk)
	scheduler.SetSettingsReader(db)
	scheduler.SetSelfUpdater(selfUpdater)
	scheduler.SetReadyGate(scanGate)
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

	// Post-scan callback: metrics textfile + agent auto-update check.
	var autoUpdateRunning atomic.Bool
	scheduler.SetScanCallback(func() {
		if cfg.MetricsTextfile != "" {
			if err := metrics.WriteTextfile(cfg.MetricsTextfile); err != nil {
				log.Warn("failed to write metrics textfile", "path", cfg.MetricsTextfile, "error", err)
			}
		}

		// Check whether connected agents need a version update.
		// Guard against overlapping runs — if a previous check is still
		// in progress (slow image pull, etc.), skip this one.
		if autoUpdate, _ := db.LoadSetting(store.SettingClusterAutoUpdateAgents); autoUpdate == "true" {
			cm.mu.Lock()
			srv := cm.srv
			cm.mu.Unlock()
			if srv != nil && autoUpdateRunning.CompareAndSwap(false, true) {
				go func() {
					defer autoUpdateRunning.Store(false)
					srv.CheckAgentVersions(ctx, version)
				}()
			}
		}
	})

	// Portainer multi-instance migration + init.
	if migrated, err := db.MigratePortainerSettings(); err != nil {
		log.Error("portainer settings migration failed", "error", err)
	} else if migrated {
		if err := db.MigratePortainerKeys("p1"); err != nil {
			log.Error("portainer key migration failed", "error", err)
		} else {
			log.Info("migrated old portainer settings to instance record")
		}
	}

	portainerAdapter := newMultiPortainerAdapter()
	portainerAdapter.engine = updater
	portainerAdapter.store = db
	var enginePortainerInstances []engine.PortainerInstance
	instances, _ := db.ListPortainerInstances()
	for _, inst := range instances {
		if !inst.Enabled || inst.URL == "" || inst.Token == "" {
			continue
		}
		pc := portainerpkg.NewClient(inst.URL, inst.Token)
		ps := portainerpkg.NewScanner(pc)
		portainerAdapter.Set(inst.ID, ps)

		// Build engine instance with endpoint configs.
		ei := engine.PortainerInstance{
			ID:      inst.ID,
			Name:    inst.Name,
			Scanner: &portainerScannerAdapter{scanner: ps},
		}
		if len(inst.Endpoints) > 0 {
			ei.Endpoints = make(map[int]engine.EndpointConfig)
			for k, v := range inst.Endpoints {
				epID, _ := strconv.Atoi(k)
				ei.Endpoints[epID] = engine.EndpointConfig{
					Enabled: v.Enabled,
					Blocked: v.Blocked,
				}
			}
		}
		enginePortainerInstances = append(enginePortainerInstances, ei)
		log.Info("portainer instance loaded", "id", inst.ID, "name", inst.Name, "url", inst.URL)
	}
	if len(enginePortainerInstances) > 0 {
		updater.SetPortainerInstances(enginePortainerInstances)
	}

	// NPM integration.
	npmURL := cfg.NPMURL
	npmEmail := cfg.NPMEmail
	npmPassword := cfg.NPMPassword
	if npmURL == "" {
		if v, err := db.LoadSetting(store.SettingNPMURL); err == nil && v != "" {
			npmURL = v
		}
	}
	if npmEmail == "" {
		if v, err := db.LoadSetting(store.SettingNPMEmail); err == nil && v != "" {
			npmEmail = v
		}
	}
	if npmPassword == "" {
		if v, err := db.LoadSetting(store.SettingNPMPassword); err == nil && v != "" {
			npmPassword = v
		}
	}
	npmEnabled, _ := db.LoadSetting(store.SettingNPMEnabled)
	npmLocalAddrs := npm.DetectLocalAddrs(cfg.HostAddress)
	log.Info("npm local address detection", "count", len(npmLocalAddrs), "sentinel_host", cfg.HostAddress)
	var npmProvider *npmAdapter
	if npmURL != "" && npmEmail != "" && npmPassword != "" && npmEnabled == "true" {
		npmClient := npm.NewClient(npmURL, npmEmail, npmPassword)
		npmResolver := npm.NewResolver(npmClient, npmLocalAddrs, log.Logger)
		go npmResolver.Run(ctx)
		npmProvider = &npmAdapter{resolver: npmResolver}
		log.Info("npm integration enabled", "url", npmURL)
	}

	// Initialise backup manager.
	backupDir := filepath.Join(filepath.Dir(cfg.DBPath), "backups")
	if v := loadSettingStr(db, "backup_dir"); v != "" {
		backupDir = v
	}
	backupMgr, backupErr := backup.NewManager(db.DB(), backupDir, log)
	if backupErr != nil {
		log.Warn("backup manager init failed", "error", backupErr)
	}

	// Configure S3 upload if settings exist.
	if backupMgr != nil {
		if raw := loadSettingStr(db, "backup_s3_config"); raw != "" {
			var s3cfg backup.S3Config
			if json.Unmarshal([]byte(raw), &s3cfg) == nil && s3cfg.Endpoint != "" {
				uploader, s3Err := backup.NewS3Uploader(s3cfg)
				if s3Err != nil {
					log.Warn("S3 uploader init failed", "error", s3Err)
				} else {
					backupMgr.SetUploader(uploader)
					log.Info("backup S3 upload enabled", "endpoint", s3cfg.Endpoint, "bucket", s3cfg.Bucket)
				}
			}
		}
		if ret := loadSettingStr(db, "backup_retention"); ret != "" {
			if n, err := strconv.Atoi(ret); err == nil && n > 0 {
				backupMgr.SetRetention(n)
			}
		}
	}

	// Start backup scheduler if configured.
	var backupSched *backup.Scheduler
	if backupMgr != nil {
		backupSched = backup.NewScheduler(backupMgr, log)
		if sched := loadSettingStr(db, "backup_schedule"); sched != "" {
			if err := backupSched.SetSchedule(sched); err != nil {
				log.Warn("backup schedule invalid", "cron", sched, "error", err)
			}
		}
		backupSched.Start()
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
			TagLister:           &tagListerAdapter{log: log},
			RegistryChecker:     &registryCheckerAdapter{checker: checker},
			VersionScope:        &versionScopeAdapter{checker: checker},
			Policy:              &policyStoreAdapter{db},
			EventLog:            &eventLogAdapter{db},
			Scheduler:           scheduler,
			SettingsStore:       &settingsStoreAdapter{db},
			Stopper:             &stopAdapter{client},
			Starter:             &startAdapter{client},
			LogViewer:           &dockerAdapter{client},
			LogStreamer:         &dockerAdapter{client},
			SelfUpdater:         &selfUpdateAdapter{updater: selfUpdater},
			NotifyConfig:        &notifyConfigAdapter{db},
			NotifyReconfigurer:  notifier,
			NotifyState:         &notifyStateAdapter{db},
			NotifyTemplateStore: &notifyTemplateAdapter{db},
			IgnoredVersions:     &ignoredVersionAdapter{db},
			RegistryCredentials: &registryCredentialAdapter{db},
			RateTracker:         &rateLimitAdapter{t: rateTracker, saver: db.SaveRateLimits},
			GHCRCache:           &ghcrCacheAdapter{c: ghcrCache},
			HookStore:           &webHookStoreAdapter{db},
			ReleaseSources:      &releaseSourceAdapter{db},
			ImageManager:        &imageAdapter{client: client},
			Cluster:             clusterCtrl,
			// Backup is set below if backupMgr is available.
			MetricsEnabled: cfg.MetricsEnabled,
			Digest:         digestSched,
			Auth:           authSvc,
			Version:        versionString(),
			ClusterPort:    cfg.ClusterPort,
			Commit:         commit,
			Log:            log.Logger,
		}
		if isSwarm {
			webDeps.Swarm = &swarmAdapter{client: client, updater: updater}
		}
		webDeps.Portainer = portainerAdapter
		webDeps.PortainerInstances = &portainerInstanceStoreAdapter{store: db}
		if npmProvider != nil {
			webDeps.NPM = npmProvider
		}
		if backupMgr != nil {
			webDeps.Backup = &backupAdapter{backupMgr}
		}
		// Factory to create NPM provider on demand (e.g. after first-time UI config).
		webDeps.NPMInitFunc = func(initCtx context.Context) (web.NPMProvider, error) {
			u, _ := db.LoadSetting(store.SettingNPMURL)
			e, _ := db.LoadSetting(store.SettingNPMEmail)
			p, _ := db.LoadSetting(store.SettingNPMPassword)
			if u == "" || e == "" || p == "" {
				return nil, fmt.Errorf("save NPM URL, email, and password first")
			}
			c := npm.NewClient(u, e, p)
			if err := c.TestConnection(initCtx); err != nil {
				return nil, err
			}
			r := npm.NewResolver(c, npmLocalAddrs, log.Logger)
			go r.Run(ctx) // use the app-level context, not the request context
			log.Info("npm integration enabled (hot)", "url", u)
			return &npmAdapter{resolver: r}, nil
		}
		webDeps.PortConfigs = &portConfigStoreAdapter{s: db}
		srv := web.NewServer(webDeps)
		srv.SetClusterLifecycle(cm)
		if freshSetup {
			srv.SetScanGate(scanGate)
		}

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

		// Configure OIDC provider if settings are present.
		// Load OIDC group mapping configuration.
		oidcGroupClaim := loadSettingStr(db, "oidc_group_claim")
		if oidcGroupClaim == "" {
			oidcGroupClaim = "groups"
		}
		var oidcGroupMappings map[string]string
		if raw := loadSettingStr(db, "oidc_group_mappings"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &oidcGroupMappings)
		}

		oidcCfg := auth.OIDCConfig{
			Enabled:       loadSettingBool(db, "oidc_enabled"),
			IssuerURL:     loadSettingStr(db, "oidc_issuer_url"),
			ClientID:      loadSettingStr(db, "oidc_client_id"),
			ClientSecret:  loadSettingStr(db, "oidc_client_secret"),
			RedirectURL:   loadSettingStr(db, "oidc_redirect_url"),
			AutoCreate:    loadSettingBool(db, "oidc_auto_create"),
			DefaultRole:   loadSettingStr(db, "oidc_default_role"),
			GroupClaim:    oidcGroupClaim,
			GroupMappings: oidcGroupMappings,
		}
		oidcProvider, oidcErr := auth.NewOIDCProvider(context.Background(), oidcCfg)
		if oidcErr != nil {
			log.Warn("OIDC provider init failed (will retry on settings save)", "error", oidcErr)
		} else if oidcProvider != nil {
			srv.SetOIDCProvider(oidcProvider)
			log.Info("OIDC SSO enabled", "issuer", oidcCfg.IssuerURL)
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

	// Flush any buffered notifications before exit.
	notifier.Stop()

	// Stop backup scheduler.
	if backupSched != nil {
		backupSched.Stop()
	}

	log.Info("sentinel shutdown complete")
}
