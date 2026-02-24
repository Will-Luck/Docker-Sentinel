package engine

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// PendingUpdate represents a container or service with an available update awaiting approval.
type PendingUpdate struct {
	ContainerID            string    `json:"container_id"`
	ContainerName          string    `json:"container_name"`
	CurrentImage           string    `json:"current_image"`
	CurrentDigest          string    `json:"current_digest"`
	RemoteDigest           string    `json:"remote_digest"`
	DetectedAt             time.Time `json:"detected_at"`
	NewerVersions          []string  `json:"newer_versions,omitempty"`
	ResolvedCurrentVersion string    `json:"resolved_current_version,omitempty"`
	ResolvedTargetVersion  string    `json:"resolved_target_version,omitempty"`
	Type                   string    `json:"type,omitempty"`    // "container" (default) or "service"
	HostID                 string    `json:"host_id,omitempty"` // cluster host ID (empty = local)
	HostName               string    `json:"host_name,omitempty"`
}

// Queue manages pending updates with BoltDB persistence.
type Queue struct {
	mu      sync.Mutex
	pending map[string]PendingUpdate // keyed by container name
	store   *store.Store
	events  *events.Bus
	log     *slog.Logger
}

// NewQueue creates a queue, optionally restoring from BoltDB.
func NewQueue(s *store.Store, bus *events.Bus, log *slog.Logger) *Queue {
	q := &Queue{
		pending: make(map[string]PendingUpdate),
		store:   s,
		events:  bus,
		log:     log,
	}

	// Restore from persistent storage.
	data, err := s.LoadPendingQueue()
	if err == nil && data != nil {
		var items []PendingUpdate
		if json.Unmarshal(data, &items) == nil {
			for _, item := range items {
				q.pending[item.Key()] = item
			}
		}
	}

	return q
}

// Add adds or replaces a pending update.
func (q *Queue) Add(update PendingUpdate) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending[update.Key()] = update
	q.persist()
	q.publishEvent(update.Key(), "added")
}

// Key returns the queue map key for this update. Remote containers use
// "hostID::name" to avoid collisions with local or other-host containers.
func (u PendingUpdate) Key() string {
	if u.HostID == "" {
		return u.ContainerName
	}
	return u.HostID + "::" + u.ContainerName
}

// Remove removes a pending update by container name.
func (q *Queue) Remove(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.pending, name)
	q.persist()
	q.publishEvent(name, "removed")
}

// Get returns a pending update by container name.
func (q *Queue) Get(name string) (PendingUpdate, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	u, ok := q.pending[name]
	return u, ok
}

// Approve atomically retrieves and removes a pending update.
// Returns the update and true if found, or zero value and false if not.
func (q *Queue) Approve(name string) (PendingUpdate, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	u, ok := q.pending[name]
	if ok {
		delete(q.pending, name)
		q.persist()
		q.publishEvent(name, "approved")
	}
	return u, ok
}

// List returns all pending updates.
func (q *Queue) List() []PendingUpdate {
	q.mu.Lock()
	defer q.mu.Unlock()
	result := make([]PendingUpdate, 0, len(q.pending))
	for _, u := range q.pending {
		result = append(result, u)
	}
	return result
}

// Len returns the number of pending updates.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// Prune removes queue entries for containers that no longer exist.
// Pass the set of currently running container names.
func (q *Queue) Prune(liveNames map[string]bool) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	var removed int
	for name := range q.pending {
		if !liveNames[name] {
			delete(q.pending, name)
			removed++
		}
	}
	if removed > 0 {
		q.persist()
		q.publishEvent("", fmt.Sprintf("pruned %d stale entries", removed))
	}
	return removed
}

// publishEvent emits a queue change SSE event if the event bus is configured.
// For remote containers the name is a scoped key ("hostID::name"); this is
// split so the SSE event carries proper HostID and ContainerName fields.
func (q *Queue) publishEvent(name, message string) {
	if q.events == nil {
		return
	}
	evt := events.SSEEvent{
		Type:          events.EventQueueChange,
		ContainerName: name,
		Message:       message,
		Timestamp:     time.Now(),
	}
	if idx := strings.Index(name, "::"); idx >= 0 {
		evt.HostID = name[:idx]
		evt.ContainerName = name[idx+2:]
	}
	q.events.Publish(evt)
}

func (q *Queue) persist() {
	items := make([]PendingUpdate, 0, len(q.pending))
	for _, u := range q.pending {
		items = append(items, u)
	}
	data, err := json.Marshal(items)
	if err != nil {
		return
	}
	if err := q.store.SavePendingQueue(data); err != nil && q.log != nil {
		q.log.Warn("failed to persist pending queue", "error", err)
	}
}
