package auth

import (
	"sync"
	"time"
)

const ceremonyTTL = 60 * time.Second

// CeremonyData holds transient WebAuthn ceremony state.
type CeremonyData struct {
	Data      interface{} // *webauthn.SessionData from go-webauthn (type-asserted by handler)
	UserID    string      // which user started this ceremony (empty for discoverable login)
	ExpiresAt time.Time
}

// CeremonyStore is a TTL-bounded in-memory store for Begin*/Finish* handoff.
type CeremonyStore struct {
	mu    sync.Mutex
	items map[string]CeremonyData
}

// NewCeremonyStore creates a new CeremonyStore and starts a background cleanup goroutine.
func NewCeremonyStore() *CeremonyStore {
	cs := &CeremonyStore{items: make(map[string]CeremonyData)}
	go cs.cleanup()
	return cs
}

// Put stores ceremony data keyed by a session ID (e.g. user ID + purpose).
func (cs *CeremonyStore) Put(key string, data interface{}, userID string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.items[key] = CeremonyData{
		Data:      data,
		UserID:    userID,
		ExpiresAt: time.Now().Add(ceremonyTTL),
	}
}

// Get retrieves and removes ceremony data. Returns nil if not found or expired.
func (cs *CeremonyStore) Get(key string) *CeremonyData {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	item, ok := cs.items[key]
	if !ok {
		return nil
	}
	delete(cs.items, key)
	if time.Now().After(item.ExpiresAt) {
		return nil
	}
	return &item
}

// cleanup removes expired entries every 30 seconds.
func (cs *CeremonyStore) cleanup() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		cs.mu.Lock()
		now := time.Now()
		for k, v := range cs.items {
			if now.After(v.ExpiresAt) {
				delete(cs.items, k)
			}
		}
		cs.mu.Unlock()
	}
}
