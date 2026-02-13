package registry

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RegistryState holds the current rate limit state for a single registry.
type RegistryState struct {
	Limit          int       `json:"limit"`           // max pulls per window; -1 = no limits detected
	Remaining      int       `json:"remaining"`       // pulls left in current window
	ResetAt        time.Time `json:"reset_at"`        // when the window resets
	IsAuth         bool      `json:"is_auth"`         // true if using stored credentials
	HasLimits      bool      `json:"has_limits"`      // false if registry doesn't return rate limit headers
	ContainerCount int       `json:"container_count"` // how many monitored containers use this registry
	LastUpdated    time.Time `json:"last_updated"`
}

// RegistryStatus is a snapshot of one registry's state for UI display.
type RegistryStatus struct {
	Registry       string    `json:"registry"`
	Limit          int       `json:"limit"`
	Remaining      int       `json:"remaining"`
	ResetAt        time.Time `json:"reset_at"`
	IsAuth         bool      `json:"is_auth"`
	HasLimits      bool      `json:"has_limits"`
	ContainerCount int       `json:"container_count"`
	LastUpdated    time.Time `json:"last_updated"`
}

// RateLimitTracker tracks per-registry rate limits in memory.
type RateLimitTracker struct {
	mu         sync.RWMutex
	registries map[string]*RegistryState
}

// NewRateLimitTracker creates a new tracker.
func NewRateLimitTracker() *RateLimitTracker {
	return &RateLimitTracker{
		registries: make(map[string]*RegistryState),
	}
}

// Discover registers a registry as known (e.g. from container image refs).
// If the registry is already tracked, only updates the container count.
func (t *RateLimitTracker) Discover(registry string, containerCount int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	registry = NormaliseRegistryHost(registry)
	if s, ok := t.registries[registry]; ok {
		s.ContainerCount = containerCount
		return
	}
	t.registries[registry] = &RegistryState{
		Limit:          -1, // unknown until first HTTP response
		ContainerCount: containerCount,
	}
}

// SetAuth marks whether a registry is using stored credentials.
func (t *RateLimitTracker) SetAuth(registry string, isAuth bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	registry = NormaliseRegistryHost(registry)
	if s, ok := t.registries[registry]; ok {
		s.IsAuth = isAuth
	}
}

// Record captures rate limit headers from a registry HTTP response.
// Auto-discovers the registry if not already tracked.
func (t *RateLimitTracker) Record(registry string, headers http.Header) {
	t.mu.Lock()
	defer t.mu.Unlock()
	registry = NormaliseRegistryHost(registry)

	s, ok := t.registries[registry]
	if !ok {
		s = &RegistryState{Limit: -1}
		t.registries[registry] = s
	}

	s.LastUpdated = time.Now()

	// Try Docker Hub format: RateLimit-Limit, RateLimit-Remaining
	if limit := headers.Get("RateLimit-Limit"); limit != "" {
		s.HasLimits = true
		s.Limit = parseRateLimitValue(limit)
		if rem := headers.Get("RateLimit-Remaining"); rem != "" {
			s.Remaining = parseRateLimitValue(rem)
		}
		// Docker Hub uses w= param for window seconds
		if window := parseRateLimitWindow(limit); window > 0 {
			s.ResetAt = time.Now().Add(time.Duration(window) * time.Second)
		}
		return
	}

	// Try GitHub/GHCR format: X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset
	if limit := headers.Get("X-RateLimit-Limit"); limit != "" {
		s.HasLimits = true
		s.Limit, _ = strconv.Atoi(limit)
		if rem := headers.Get("X-RateLimit-Remaining"); rem != "" {
			s.Remaining, _ = strconv.Atoi(rem)
		}
		if reset := headers.Get("X-RateLimit-Reset"); reset != "" {
			if epoch, err := strconv.ParseInt(reset, 10, 64); err == nil {
				s.ResetAt = time.Unix(epoch, 0)
			}
		}
		return
	}

	// No rate limit headers detected
	if !s.HasLimits && s.Limit == -1 {
		s.HasLimits = false
	}
}

// CanProceed checks if we can make another request to a registry.
// reserve is the minimum remaining pulls to keep as headroom.
// Returns (canProceed, waitDuration).
func (t *RateLimitTracker) CanProceed(registry string, reserve int) (bool, time.Duration) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	registry = NormaliseRegistryHost(registry)

	s, ok := t.registries[registry]
	if !ok {
		return true, 0 // unknown registry — allow
	}
	if !s.HasLimits {
		return true, 0 // no limits detected — allow
	}
	if s.Remaining > reserve {
		return true, 0
	}
	// Rate limited — calculate wait duration
	wait := time.Until(s.ResetAt)
	if wait < 0 {
		// Reset time has passed, allow (stale data)
		return true, 0
	}
	return false, wait
}

// Status returns a snapshot of all tracked registries for UI display.
func (t *RateLimitTracker) Status() []RegistryStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]RegistryStatus, 0, len(t.registries))
	for host, s := range t.registries {
		result = append(result, RegistryStatus{
			Registry:       host,
			Limit:          s.Limit,
			Remaining:      s.Remaining,
			ResetAt:        s.ResetAt,
			IsAuth:         s.IsAuth,
			HasLimits:      s.HasLimits,
			ContainerCount: s.ContainerCount,
			LastUpdated:    s.LastUpdated,
		})
	}
	return result
}

// OverallHealth returns the worst state across all registries.
// "ok" = all above 20%, "low" = any below 20%, "exhausted" = any at 0.
func (t *RateLimitTracker) OverallHealth() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	health := "ok"
	for _, s := range t.registries {
		if !s.HasLimits || s.Limit <= 0 {
			continue
		}
		// Check if reset has passed — stale data = ok
		if !s.ResetAt.IsZero() && time.Now().After(s.ResetAt) {
			continue
		}
		pct := float64(s.Remaining) / float64(s.Limit)
		if s.Remaining <= 0 {
			return "exhausted"
		}
		if pct < 0.2 {
			health = "low"
		}
	}
	return health
}

// NormaliseRegistryHost maps registry host variants to a canonical form.
// "registry-1.docker.io" and "index.docker.io" both map to "docker.io".
func NormaliseRegistryHost(host string) string {
	switch host {
	case "registry-1.docker.io", "index.docker.io", "docker.io":
		return "docker.io"
	}
	return host
}

// parseRateLimitValue extracts the numeric value from a Docker Hub rate limit header.
// e.g. "100;w=21600" → 100
func parseRateLimitValue(val string) int {
	parts := strings.SplitN(val, ";", 2)
	n, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
	return n
}

// parseRateLimitWindow extracts the window seconds from a Docker Hub rate limit header.
// e.g. "100;w=21600" → 21600
func parseRateLimitWindow(val string) int {
	parts := strings.SplitN(val, ";", 2)
	if len(parts) < 2 {
		return 0
	}
	kv := strings.TrimSpace(parts[1])
	if strings.HasPrefix(kv, "w=") {
		n, _ := strconv.Atoi(kv[2:])
		return n
	}
	return 0
}
