package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/GiteaLN/Docker-Sentinel/internal/clock"
	"github.com/GiteaLN/Docker-Sentinel/internal/config"
	"github.com/GiteaLN/Docker-Sentinel/internal/docker"
	"github.com/GiteaLN/Docker-Sentinel/internal/engine"
	"github.com/GiteaLN/Docker-Sentinel/internal/logging"
	"github.com/GiteaLN/Docker-Sentinel/internal/registry"
	"github.com/GiteaLN/Docker-Sentinel/internal/store"
)

var version = "dev"

func main() {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(1)
	}
	log := logging.New(cfg.LogJSON)

	fmt.Println("Docker-Sentinel " + version)
	fmt.Println("=============================================")
	fmt.Printf("SENTINEL_POLL_INTERVAL=%s\n", cfg.PollInterval)
	fmt.Printf("SENTINEL_GRACE_PERIOD=%s\n", cfg.GracePeriod)
	fmt.Printf("SENTINEL_DEFAULT_POLICY=%s\n", cfg.DefaultPolicy)
	fmt.Printf("SENTINEL_DB_PATH=%s\n", cfg.DBPath)

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

	clk := clock.Real{}
	checker := registry.NewChecker(client, log)
	queue := engine.NewQueue(db)
	updater := engine.NewUpdater(client, checker, db, queue, cfg, log, clk)
	scheduler := engine.NewScheduler(updater, cfg, log, clk)

	log.Info("sentinel started", "version", version)

	if err := scheduler.Run(ctx); err != nil {
		log.Error("sentinel exited with error", "error", err)
		os.Exit(1)
	}

	log.Info("sentinel shutdown complete")
}
