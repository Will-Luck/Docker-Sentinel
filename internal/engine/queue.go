package engine

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/GiteaLN/Docker-Sentinel/internal/store"
)

// PendingUpdate represents a container with an available update awaiting approval.
type PendingUpdate struct {
	ContainerID   string    `json:"container_id"`
	ContainerName string    `json:"container_name"`
	CurrentImage  string    `json:"current_image"`
	CurrentDigest string    `json:"current_digest"`
	RemoteDigest  string    `json:"remote_digest"`
	DetectedAt    time.Time `json:"detected_at"`
}

// Queue manages pending updates with BoltDB persistence.
type Queue struct {
	mu      sync.Mutex
	pending map[string]PendingUpdate // keyed by container name
	store   *store.Store
}

// NewQueue creates a queue, optionally restoring from BoltDB.
func NewQueue(s *store.Store) *Queue {
	q := &Queue{
		pending: make(map[string]PendingUpdate),
		store:   s,
	}

	// Restore from persistent storage.
	data, err := s.LoadPendingQueue()
	if err == nil && data != nil {
		var items []PendingUpdate
		if json.Unmarshal(data, &items) == nil {
			for _, item := range items {
				q.pending[item.ContainerName] = item
			}
		}
	}

	return q
}

// Add adds or replaces a pending update.
func (q *Queue) Add(update PendingUpdate) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending[update.ContainerName] = update
	q.persist()
}

// Remove removes a pending update by container name.
func (q *Queue) Remove(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.pending, name)
	q.persist()
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

func (q *Queue) persist() {
	items := make([]PendingUpdate, 0, len(q.pending))
	for _, u := range q.pending {
		items = append(items, u)
	}
	data, err := json.Marshal(items)
	if err != nil {
		return
	}
	_ = q.store.SavePendingQueue(data)
}
