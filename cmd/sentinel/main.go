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
	"strings"
	"syscall"

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
	bus := events.New()
	queue := engine.NewQueue(db, bus)
	updater := engine.NewUpdater(client, checker, db, queue, cfg, log, clk, notifier, bus)
	scheduler := engine.NewScheduler(updater, cfg, log, clk)
	// Start web dashboard if enabled.
	if cfg.WebEnabled {
		srv := web.NewServer(web.Dependencies{
			Store:           &storeAdapter{db},
			Queue:           &queueAdapter{queue},
			Docker:          &dockerAdapter{client},
			Updater:         updater,
			Config:          cfg,
			EventBus:        bus,
			Snapshots:       &snapshotAdapter{db},
			Rollback:        &rollbackAdapter{d: client, s: db, log: log},
			Restarter:       &restartAdapter{client},
			Registry:        &registryAdapter{log: log},
			RegistryChecker: &registryCheckerAdapter{checker: checker},
			Policy:          &policyStoreAdapter{db},
			EventLog:        &eventLogAdapter{db},
			Log:             log.Logger,
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
		ContainerID:   update.ContainerID,
		ContainerName: update.ContainerName,
		CurrentImage:  update.CurrentImage,
		CurrentDigest: update.CurrentDigest,
		RemoteDigest:  update.RemoteDigest,
		DetectedAt:    update.DetectedAt,
		NewerVersions: update.NewerVersions,
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
		ContainerID:   item.ContainerID,
		ContainerName: item.ContainerName,
		CurrentImage:  item.CurrentImage,
		CurrentDigest: item.CurrentDigest,
		RemoteDigest:  item.RemoteDigest,
		DetectedAt:    item.DetectedAt,
		NewerVersions: item.NewerVersions,
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
	tags, err := registry.ListTags(ctx, imageRef, token)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	// Filter to semver-parseable tags and return newest first.
	newer := registry.NewerVersions(tag, tags)
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

func (a *registryCheckerAdapter) CheckForUpdate(ctx context.Context, imageRef string) (bool, []string, error) {
	result := a.checker.CheckVersioned(ctx, imageRef)
	if result.Error != nil {
		return false, nil, result.Error
	}
	if result.IsLocal {
		return false, nil, nil
	}
	return result.UpdateAvailable, result.NewerVersions, nil
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
