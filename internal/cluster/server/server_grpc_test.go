package server

import (
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// mockHistoryRecorder captures RecordUpdate calls for test assertions.
type mockHistoryRecorder struct {
	mu      sync.Mutex
	records []store.UpdateRecord
	err     error // if set, RecordUpdate returns this error
}

func (m *mockHistoryRecorder) RecordUpdate(rec store.UpdateRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.records = append(m.records, rec)
	return nil
}

func (m *mockHistoryRecorder) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}

func (m *mockHistoryRecorder) all() []store.UpdateRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]store.UpdateRecord, len(m.records))
	copy(cp, m.records)
	return cp
}

// journalTestServer creates a minimal Server with a registry, event bus, and
// optional HistoryRecorder. No gRPC listener — only used for unit testing
// handleOfflineJournal directly.
func journalTestServer(t *testing.T, hist HistoryRecorder) *Server {
	t.Helper()

	// Use errStore as a no-op ClusterStore since we only need the registry.
	cs := &errStore{}
	bus := events.New()
	log := slog.Default()
	reg := NewRegistry(cs, log.With("component", "test-registry"))

	return &Server{
		registry: reg,
		history:  hist,
		bus:      bus,
		log:      log.With("component", "test-server"),
		streams:  make(map[string]*agentStream),
		pending:  make(map[string]chan *proto.AgentMessage),
	}
}

// registerTestHost adds a host to the registry so handleOfflineJournal
// can look up the host name.
func registerTestHost(t *testing.T, srv *Server, id, name string) {
	t.Helper()
	err := srv.registry.Register(cluster.HostInfo{
		ID:   id,
		Name: name,
	})
	if err != nil {
		t.Fatalf("register test host: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestHandleOfflineJournal_PersistsHistory verifies that each journal entry
// is persisted via RecordUpdate with the correct field mapping.
// ---------------------------------------------------------------------------

func TestHandleOfflineJournal_PersistsHistory(t *testing.T) {
	hist := &mockHistoryRecorder{}
	srv := journalTestServer(t, hist)
	registerTestHost(t, srv, "host-1", "worker-node")

	ts := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	dur := 5 * time.Second

	journal := &proto.OfflineJournal{
		Entries: []*proto.JournalEntry{
			{
				Id:        "j1",
				Timestamp: timestamppb.New(ts),
				Action:    "update",
				Container: "nginx",
				OldImage:  "nginx:1.24",
				NewImage:  "nginx:1.25",
				OldDigest: "sha256:aaa",
				NewDigest: "sha256:bbb",
				Outcome:   "success",
				Duration:  durationpb.New(dur),
			},
		},
	}

	srv.handleOfflineJournal("host-1", journal)

	if hist.count() != 1 {
		t.Fatalf("expected 1 record, got %d", hist.count())
	}

	rec := hist.all()[0]

	if rec.Timestamp != ts {
		t.Errorf("Timestamp: got %v, want %v", rec.Timestamp, ts)
	}
	if rec.ContainerName != "nginx" {
		t.Errorf("ContainerName: got %q, want %q", rec.ContainerName, "nginx")
	}
	if rec.OldImage != "nginx:1.24" {
		t.Errorf("OldImage: got %q, want %q", rec.OldImage, "nginx:1.24")
	}
	if rec.NewImage != "nginx:1.25" {
		t.Errorf("NewImage: got %q, want %q", rec.NewImage, "nginx:1.25")
	}
	if rec.OldDigest != "sha256:aaa" {
		t.Errorf("OldDigest: got %q, want %q", rec.OldDigest, "sha256:aaa")
	}
	if rec.NewDigest != "sha256:bbb" {
		t.Errorf("NewDigest: got %q, want %q", rec.NewDigest, "sha256:bbb")
	}
	if rec.Outcome != "success" {
		t.Errorf("Outcome: got %q, want %q", rec.Outcome, "success")
	}
	if rec.Duration != dur {
		t.Errorf("Duration: got %v, want %v", rec.Duration, dur)
	}
	if rec.HostID != "host-1" {
		t.Errorf("HostID: got %q, want %q", rec.HostID, "host-1")
	}
	if rec.HostName != "worker-node" {
		t.Errorf("HostName: got %q, want %q", rec.HostName, "worker-node")
	}
}

// ---------------------------------------------------------------------------
// TestHandleOfflineJournal_NilHistory verifies that a nil HistoryRecorder
// does not cause a panic and SSE events are still published.
// ---------------------------------------------------------------------------

func TestHandleOfflineJournal_NilHistory(t *testing.T) {
	srv := journalTestServer(t, nil) // nil history
	registerTestHost(t, srv, "host-2", "edge-node")

	journal := &proto.OfflineJournal{
		Entries: []*proto.JournalEntry{
			{
				Id:        "j2",
				Timestamp: timestamppb.Now(),
				Action:    "update",
				Container: "redis",
				Outcome:   "success",
			},
		},
	}

	// Subscribe before calling handleOfflineJournal so we catch the event.
	evtCh, cancel := srv.bus.Subscribe()
	defer cancel()

	// Should not panic.
	srv.handleOfflineJournal("host-2", journal)

	// Verify SSE event was still published.
	select {
	case evt := <-evtCh:
		if evt.Type != events.EventContainerUpdate {
			t.Errorf("event type: got %q, want %q", evt.Type, events.EventContainerUpdate)
		}
		if evt.ContainerName != "redis" {
			t.Errorf("event container: got %q, want %q", evt.ContainerName, "redis")
		}
		if evt.HostID != "host-2" {
			t.Errorf("event HostID: got %q, want %q", evt.HostID, "host-2")
		}
		if evt.HostName != "edge-node" {
			t.Errorf("event HostName: got %q, want %q", evt.HostName, "edge-node")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for SSE event")
	}
}

// ---------------------------------------------------------------------------
// TestHandleOfflineJournal_MixedOutcomes verifies that both successful and
// failed entries are persisted, with the failed entry's error message
// included in the SSE event.
// ---------------------------------------------------------------------------

func TestHandleOfflineJournal_MixedOutcomes(t *testing.T) {
	hist := &mockHistoryRecorder{}
	srv := journalTestServer(t, hist)
	registerTestHost(t, srv, "host-3", "gpu-node")

	evtCh, cancel := srv.bus.Subscribe()
	defer cancel()

	journal := &proto.OfflineJournal{
		Entries: []*proto.JournalEntry{
			{
				Id:        "j3",
				Timestamp: timestamppb.Now(),
				Action:    "update",
				Container: "postgres",
				OldImage:  "postgres:15",
				NewImage:  "postgres:16",
				Outcome:   "success",
				Duration:  durationpb.New(3 * time.Second),
			},
			{
				Id:        "j4",
				Timestamp: timestamppb.Now(),
				Action:    "update",
				Container: "grafana",
				OldImage:  "grafana:10.0",
				NewImage:  "grafana:10.1",
				Outcome:   "failed",
				Error:     "image pull timeout",
				Duration:  durationpb.New(30 * time.Second),
			},
		},
	}

	srv.handleOfflineJournal("host-3", journal)

	// Both entries should be persisted.
	if hist.count() != 2 {
		t.Fatalf("expected 2 records, got %d", hist.count())
	}

	records := hist.all()

	// First: success
	if records[0].Outcome != "success" {
		t.Errorf("record[0] Outcome: got %q, want %q", records[0].Outcome, "success")
	}
	if records[0].ContainerName != "postgres" {
		t.Errorf("record[0] ContainerName: got %q, want %q", records[0].ContainerName, "postgres")
	}

	// Second: failed with error
	if records[1].Outcome != "failed" {
		t.Errorf("record[1] Outcome: got %q, want %q", records[1].Outcome, "failed")
	}
	if records[1].Error != "image pull timeout" {
		t.Errorf("record[1] Error: got %q, want %q", records[1].Error, "image pull timeout")
	}

	// Verify SSE events: collect two events.
	var evts []events.SSEEvent
	deadline := time.After(time.Second)
	for len(evts) < 2 {
		select {
		case evt := <-evtCh:
			evts = append(evts, evt)
		case <-deadline:
			t.Fatalf("timeout: got %d events, expected 2", len(evts))
		}
	}

	// The failed entry's SSE message should mention the error.
	var foundFailMsg bool
	for _, evt := range evts {
		if evt.ContainerName == "grafana" {
			if evt.Message == "" {
				t.Error("expected non-empty message for failed entry")
			}
			foundFailMsg = true
		}
	}
	if !foundFailMsg {
		t.Error("expected an SSE event for the failed grafana entry")
	}
}

// ---------------------------------------------------------------------------
// TestHandleOfflineJournal_RecordUpdateError verifies that a failing
// HistoryRecorder does not prevent subsequent entries from being processed.
// ---------------------------------------------------------------------------

func TestHandleOfflineJournal_RecordUpdateError(t *testing.T) {
	hist := &mockHistoryRecorder{err: fmt.Errorf("disk full")}
	srv := journalTestServer(t, hist)
	registerTestHost(t, srv, "host-4", "broken-node")

	evtCh, cancel := srv.bus.Subscribe()
	defer cancel()

	journal := &proto.OfflineJournal{
		Entries: []*proto.JournalEntry{
			{
				Id:        "j5",
				Timestamp: timestamppb.Now(),
				Container: "app",
				Outcome:   "success",
			},
			{
				Id:        "j6",
				Timestamp: timestamppb.Now(),
				Container: "db",
				Outcome:   "success",
			},
		},
	}

	// Should not panic despite RecordUpdate failing.
	srv.handleOfflineJournal("host-4", journal)

	// SSE events should still be published for both entries.
	var count int
	deadline := time.After(time.Second)
	for count < 2 {
		select {
		case <-evtCh:
			count++
		case <-deadline:
			t.Fatalf("expected 2 SSE events despite history errors, got %d", count)
		}
	}
}
