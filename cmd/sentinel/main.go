package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/GiteaLN/Docker-Sentinel/internal/clock"
	"github.com/GiteaLN/Docker-Sentinel/internal/config"
	"github.com/GiteaLN/Docker-Sentinel/internal/docker"
	"github.com/GiteaLN/Docker-Sentinel/internal/engine"
	"github.com/GiteaLN/Docker-Sentinel/internal/logging"
	"github.com/GiteaLN/Docker-Sentinel/internal/notify"
	"github.com/GiteaLN/Docker-Sentinel/internal/registry"
	"github.com/GiteaLN/Docker-Sentinel/internal/store"
	"github.com/GiteaLN/Docker-Sentinel/internal/web"
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
	fmt.Printf("SENTINEL_WEB_ENABLED=%t\n", cfg.WebEnabled)
	fmt.Printf("SENTINEL_WEB_PORT=%s\n", cfg.WebPort)

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

	// Build notification chain.
	var notifiers []notify.Notifier
	notifiers = append(notifiers, notify.NewLogNotifier(log))
	if cfg.GotifyURL != "" {
		notifiers = append(notifiers, notify.NewGotify(cfg.GotifyURL, cfg.GotifyToken))
		log.Info("gotify notifications enabled", "url", cfg.GotifyURL)
	}
	if cfg.WebhookURL != "" {
		headers := parseHeaders(cfg.WebhookHeaders)
		notifiers = append(notifiers, notify.NewWebhook(cfg.WebhookURL, headers))
		log.Info("webhook notifications enabled", "url", cfg.WebhookURL)
	}
	notifier := notify.NewMulti(log, notifiers...)

	clk := clock.Real{}
	checker := registry.NewChecker(client, log)
	queue := engine.NewQueue(db)
	updater := engine.NewUpdater(client, checker, db, queue, cfg, log, clk, notifier)
	scheduler := engine.NewScheduler(updater, cfg, log, clk)

	// Start web dashboard if enabled.
	if cfg.WebEnabled {
		srv := web.NewServer(web.Dependencies{
			Store:   &storeAdapter{db},
			Queue:   &queueAdapter{queue},
			Docker:  &dockerAdapter{client},
			Updater: updater,
			Config:  cfg,
			Log:     log.Logger,
		})

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

func (a *storeAdapter) GetMaintenance(name string) (bool, error) {
	return a.s.GetMaintenance(name)
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
		ContainerID:   item.ContainerID,
		ContainerName: item.ContainerName,
		CurrentImage:  item.CurrentImage,
		CurrentDigest: item.CurrentDigest,
		RemoteDigest:  item.RemoteDigest,
		DetectedAt:    item.DetectedAt,
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
