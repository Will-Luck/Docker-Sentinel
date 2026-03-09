package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/agent"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/web"
)

// runAgent starts Sentinel in agent mode. This is a completely separate
// code path from the server — it connects to a remote Sentinel server
// over gRPC and executes update commands on the local Docker host.
func runAgent(ctx context.Context, cfg *config.Config, log *logging.Logger) {
	fmt.Println("Docker-Sentinel Agent " + versionString())
	fmt.Println("=============================================")
	fmt.Printf("SENTINEL_SERVER_ADDR=%s\n", cfg.ServerAddr)
	fmt.Printf("SENTINEL_HOST_NAME=%s\n", cfg.HostName)
	fmt.Printf("SENTINEL_CLUSTER_DIR=%s\n", cfg.ClusterDataDir)

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Load Docker TLS settings for agent mode too.
	var agentTLSCfg *docker.TLSConfig
	agentCA, _ := db.LoadSetting(store.SettingDockerTLSCA)
	agentCert, _ := db.LoadSetting(store.SettingDockerTLSCert)
	agentKey, _ := db.LoadSetting(store.SettingDockerTLSKey)
	if agentCA != "" && agentCert != "" && agentKey != "" {
		agentTLSCfg = &docker.TLSConfig{CACert: agentCA, ClientCert: agentCert, ClientKey: agentKey}
		log.Info("Docker TLS configured (agent)", "ca", agentCA, "cert", agentCert)
	}

	client, err := docker.NewClient(cfg.DockerSock, agentTLSCfg)
	if err != nil {
		log.Error("failed to create Docker client", "error", err)
		os.Exit(1)
	}
	defer client.Close()

	if err := db.EnsureAuthBuckets(); err != nil {
		log.Error("failed to create auth buckets", "error", err)
		os.Exit(1)
	}

	authSvc := auth.NewService(auth.ServiceConfig{
		Users:          db,
		Sessions:       db,
		Roles:          db,
		Tokens:         db,
		Settings:       db,
		PendingTOTP:    db,
		Log:            log.Logger,
		CookieSecure:   cfg.CookieSecure,
		SessionExpiry:  cfg.SessionExpiry,
		AuthEnabledEnv: cfg.AuthEnabled,
	})

	agentWeb := web.NewAgentServer(web.AgentDeps{
		Auth:          authSvc,
		SettingsStore: &settingsStoreAdapter{db},
		Log:           log.Logger,
		Version:       versionString(),
	})

	go func() {
		addr := net.JoinHostPort("", cfg.WebPort)
		if err := agentWeb.ListenAndServe(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("agent web server error", "error", err)
		}
	}()

	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = agentWeb.Shutdown(shutCtx)
	}()

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
	agentWeb.SetStatusProvider(a)

	log.Info("starting agent mode", "server", cfg.ServerAddr, "host", cfg.HostName)
	if err := a.Run(ctx); err != nil {
		log.Error("agent exited with error", "error", err)
		os.Exit(1)
	}
	log.Info("agent shutdown complete")
}

func generateRandomPassword() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
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

// loadSettingStr loads a setting from the DB, returning "" on error.
func loadSettingStr(db *store.Store, key string) string {
	v, err := db.LoadSetting(key)
	if err != nil {
		return ""
	}
	return v
}

// loadSettingBool loads a boolean setting from the DB.
func loadSettingBool(db *store.Store, key string) bool {
	return loadSettingStr(db, key) == "true"
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
