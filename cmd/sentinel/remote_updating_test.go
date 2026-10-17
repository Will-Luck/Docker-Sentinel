package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/web"
)

// remoteRun is what a remote update or rollback is started with: the hub
// (through its cluster adapter), the agent's host and the dashboard's URL.
type remoteRun struct {
	a         *clusterAdapter
	hostID    string
	dashboard string
}

// dashboardClient asks the test dashboards. Without keep-alives it leaves
// no connection open that never carried a request, which a dashboard's
// shutdown would wait 5 seconds for.
var dashboardClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

// dashboardOver serves the dashboard over ctrl, as main does, with sign-in
// turned off, and returns its URL.
func dashboardOver(t *testing.T, ctrl *web.ClusterController, a *clusterAdapter, db *store.Store, bus *events.Bus) string {
	t.Helper()
	if err := db.SaveSetting("auth_setup_complete", "true"); err != nil {
		t.Fatal(err)
	}
	off := false
	discard := slog.New(slog.DiscardHandler)
	srv := web.NewServer(web.Dependencies{
		Store:         &storeAdapter{db},
		Queue:         &queueAdapter{engine.NewQueue(db, bus, discard)},
		EventBus:      bus,
		Rollback:      &rollbackAdapter{updater: a.updater},
		Policy:        &policyStoreAdapter{db},
		EventLog:      &eventLogAdapter{db},
		SettingsStore: &settingsStoreAdapter{db},
		Cluster:       ctrl,
		Auth: auth.NewService(auth.ServiceConfig{
			Users: db, Sessions: db, Roles: db, Tokens: db, Settings: db, PendingTOTP: db,
			Log: discard, AuthEnabledEnv: &off,
		}),
		Log: discard,
	})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	lis.Close()
	go func() { _ = srv.ListenAndServe(addr) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	url := "http://" + addr
	waitUntil(t, 5*time.Second, func() bool {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/static/style.css", nil)
		if err != nil {
			return false
		}
		resp, err := dashboardClient.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	}, "the dashboard should listen")
	return url
}

// post sends a dashboard request and wants it accepted.
func post(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := dashboardClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s: %d %s", url, resp.StatusCode, body)
	}
	return nil
}

// Every remote update and rollback is announced once, by the hub, whoever
// started it: the scan (auto policy), the dashboard and the API. While the
// agent works on it, the dashboard's question (is it in flight?) says yes,
// so every open browser shows "Updating" from start to end; once it is out of
// flight a state event has every browser fetch the row again. A rollback is
// told as a rollback. One the agent answers with a failure ends with the
// hub's event only: the dashboard that started it does not tell it again.
func TestRemoteUpdates_AnnouncedOnceAndInFlightWhoeverStartsThem(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failed    bool // the agent answers that it failed
		start     func(ctx context.Context, run remoteRun) error
		wantStart string
		wantEnd   string
	}{
		{"scan", false, func(ctx context.Context, run remoteRun) error {
			_, err := (&clusterScannerAdapter{srv: run.a.srv}).UpdateContainer(ctx, run.hostID, "nginx", "nginx:1.27", "")
			return err
		}, "update started", "update nginx: success"},
		{"dashboard or API", false, func(ctx context.Context, run remoteRun) error {
			_, err := run.a.UpdateRemoteContainer(ctx, run.hostID, "nginx", "nginx:1.27", "")
			return err
		}, "update started", "update nginx: success"},
		{"rollback", false, func(ctx context.Context, run remoteRun) error {
			return run.a.RollbackRemoteContainer(ctx, run.hostID, "nginx")
		}, "rollback started", "rollback nginx: success"},
		{"failed update from the dashboard", true, func(ctx context.Context, run remoteRun) error {
			return post(ctx, run.dashboard+"/api/update/nginx?host="+run.hostID)
		}, "update started", "update nginx failed: pull failed"},
		{"failed rollback from the dashboard", true, func(ctx context.Context, run remoteRun) error {
			return post(ctx, run.dashboard+"/api/containers/nginx/rollback?host="+run.hostID)
		}, "rollback started", "rollback nginx failed: pull failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := make(chan struct{}, 1)
			release := make(chan struct{})
			bus := events.New()
			report := &proto.StateReport{AgentVersion: "3.1.0", Containers: []*proto.ContainerInfo{
				{Id: "c1", Name: "nginx", Image: "nginx:1.26", State: "running"},
			}}
			a, hostID, db, _ := hubOnBus(t, bus, report, func(req *proto.UpdateContainerRequest) *proto.UpdateResult {
				asked <- struct{}{}
				<-release
				ur := &proto.UpdateResult{ContainerName: req.ContainerName, OldImage: "nginx:1.26", NewImage: req.TargetImage, Outcome: "success"}
				if tc.failed {
					ur.Outcome, ur.Error = "failed", "pull failed"
				}
				return ur
			})
			// What a rollback goes back to.
			if err := db.RecordUpdate(store.UpdateRecord{
				Timestamp: time.Now().Add(-time.Hour), ContainerName: store.ScopedKey(hostID, "nginx"), OldImage: "nginx:1.25",
				NewImage: "nginx:1.26", Outcome: "success", HostID: hostID, HostName: "edge",
			}); err != nil {
				t.Fatal(err)
			}
			ctrl := web.NewClusterController()
			ctrl.SetProvider(a)
			run := remoteRun{a: a, hostID: hostID, dashboard: dashboardOver(t, ctrl, a, db, bus)}
			ch, cancel := bus.Subscribe()
			defer cancel()

			ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			done := make(chan error, 1)
			go func() { done <- tc.start(ctx, run) }()
			select {
			case <-asked:
			case <-time.After(30 * time.Second):
				t.Fatal("the agent was never asked")
			}
			if !ctrl.UpdateInFlight(hostID, "nginx") {
				t.Error("the dashboard is not told the container is updating while the agent works on it")
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			var got []events.SSEEvent
			var inFlightAtState bool
			deadline := time.After(30 * time.Second)
		collect:
			for {
				select {
				case e := <-ch:
					if e.HostID != hostID || e.ContainerName != "nginx" {
						continue
					}
					got = append(got, e)
					if e.Type == events.EventContainerState {
						inFlightAtState = ctrl.UpdateInFlight(hostID, "nginx")
						break collect
					}
				case <-deadline:
					t.Fatalf("no state event after the update; events: %+v", got)
				}
			}
			if tc.failed {
				// The dashboard records the failure, then would tell it.
				waitUntil(t, 10*time.Second, func() bool {
					recs, _ := db.ListHistoryByContainer(store.ScopedKey(hostID, "nginx"), 10)
					return len(recs) > 0 && recs[0].Outcome == "failed"
				}, "the failure should be recorded")
			}
			// Anything more (a second start or result) would come at once.
			quiet := time.After(200 * time.Millisecond)
		drain:
			for {
				select {
				case e := <-ch:
					if e.HostID == hostID && e.ContainerName == "nginx" {
						got = append(got, e)
					}
				case <-quiet:
					break drain
				}
			}

			if len(got) != 3 ||
				got[0].Type != events.EventContainerUpdate || got[0].Message != tc.wantStart ||
				got[1].Type != events.EventContainerUpdate || got[1].Message != tc.wantEnd ||
				got[2].Type != events.EventContainerState {
				t.Errorf("events = %+v\nwant one container_update %q, one container_update %q, then a container_state", got, tc.wantStart, tc.wantEnd)
			}
			if inFlightAtState {
				t.Error("the state event went out while the container was still in flight")
			}
		})
	}
}

// An update the hub refuses before sending it (a paused host) is not
// announced as started, and the dashboard that asked for it tells the
// failure, once: the hub tells nothing of it.
func TestRemoteUpdate_RefusedByTheHubToldOnceByTheDashboard(t *testing.T) {
	bus := events.New()
	report := &proto.StateReport{AgentVersion: "3.1.0", Containers: []*proto.ContainerInfo{
		{Id: "c1", Name: "nginx", Image: "nginx:1.26", State: "running"},
	}}
	a, hostID, db, _ := hubOnBus(t, bus, report, func(req *proto.UpdateContainerRequest) *proto.UpdateResult {
		t.Error("a paused host was asked to update")
		return &proto.UpdateResult{ContainerName: req.ContainerName, Outcome: "success"}
	})
	if err := a.PauseHost(hostID); err != nil {
		t.Fatal(err)
	}
	ctrl := web.NewClusterController()
	ctrl.SetProvider(a)
	dashboard := dashboardOver(t, ctrl, a, db, bus)
	ch, cancel := bus.Subscribe()
	defer cancel()

	if err := post(context.Background(), dashboard+"/api/update/nginx?host="+hostID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, func() bool {
		recs, _ := db.ListHistoryByContainer(store.ScopedKey(hostID, "nginx"), 10)
		return len(recs) > 0 && recs[0].Outcome == "failed"
	}, "the refusal should be recorded")
	var updates []events.SSEEvent
	quiet := time.After(200 * time.Millisecond)
drain:
	for {
		select {
		case e := <-ch:
			if e.Type == events.EventContainerUpdate && e.HostID == hostID && e.ContainerName == "nginx" {
				updates = append(updates, e)
			}
		case <-quiet:
			break drain
		}
	}
	if len(updates) != 1 || !strings.HasPrefix(updates[0].Message, "update failed on edge: ") || !strings.Contains(updates[0].Message, "paused") {
		t.Errorf("container_update events = %+v, want one, the dashboard's, saying the host is paused", updates)
	}
}

// heldNotifier holds every "update started" notification until release is
// closed, as a slow or failing notification service does, and keeps the
// types of the notifications sent, in the order their sending ended.
type heldNotifier struct {
	release chan struct{}
	mu      sync.Mutex
	sent    []notify.EventType
}

func (h *heldNotifier) Send(ctx context.Context, e notify.Event) error {
	if e.Type == notify.EventUpdateStarted {
		select {
		case <-h.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, e.Type)
	return nil
}

func (h *heldNotifier) Name() string { return "held" }

func (h *heldNotifier) types() []notify.EventType {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]notify.EventType(nil), h.sent...)
}

// A remote update started from the dashboard or the API is announced (and
// sent to the agent) without waiting for its "update started"
// notification, which a slow or failing notification service can hold up
// for its retries. The notifications still go out in order: started, then
// the outcome.
func TestUpdateRemoteContainer_AnnouncedWithoutWaitingForTheStartNotification(t *testing.T) {
	asked := make(chan struct{}, 1)
	bus := events.New()
	a, hostID, db, _ := hubOnBus(t, bus, nil, func(req *proto.UpdateContainerRequest) *proto.UpdateResult {
		asked <- struct{}{}
		return &proto.UpdateResult{ContainerName: req.ContainerName, OldImage: "nginx:1.26", NewImage: req.TargetImage, Outcome: "success"}
	})
	held := &heldNotifier{release: make(chan struct{})}
	log := logging.New(false)
	a.updater = engine.NewUpdater(nil, nil, db, nil, config.NewTestConfig(), log, clock.Real{}, notify.NewMulti(log, held), nil)
	ch, cancel := bus.Subscribe()
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := a.UpdateRemoteContainer(context.Background(), hostID, "nginx", "nginx:1.27", "")
		done <- err
	}()
	released := false
	defer func() {
		if !released {
			close(held.release)
		}
	}()
	deadline := time.After(10 * time.Second)
	for started := false; !started; {
		select {
		case e := <-ch:
			started = e.Type == events.EventContainerUpdate && e.ContainerName == "nginx" && e.Message == "update started"
		case <-deadline:
			t.Fatal("no \"update started\" event while the start notification was being sent")
		}
	}
	select {
	case <-asked:
	case <-deadline:
		t.Fatal("the agent was not asked while the start notification was being sent")
	}
	if got := held.types(); len(got) != 0 {
		t.Fatalf("notifications sent while the start was held: %v", got)
	}
	close(held.release)
	released = true
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := held.types(); len(got) != 2 || got[0] != notify.EventUpdateStarted || got[1] != notify.EventUpdateSucceeded {
		t.Errorf("notifications = %v, want update started, then update succeeded", got)
	}
}
