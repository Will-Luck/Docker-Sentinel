package cloudauth

import (
	"context"
	"sync"
	"time"
)

// Provider authenticates against a cloud container registry.
type Provider interface {
	Name() string
	Matches(host string) bool
	GetCredentials(ctx context.Context) (username, password string, expiry time.Time, err error)
}

// Registry holds configured cloud auth providers with a token cache.
type Registry struct {
	mu        sync.RWMutex
	providers []Provider
	cache     map[string]cachedCred
}

type cachedCred struct {
	username string
	password string
	expiry   time.Time
}

func New() *Registry {
	return &Registry{
		cache: make(map[string]cachedCred),
	}
}

func (r *Registry) AddProvider(p Provider) {
	r.mu.Lock()
	r.providers = append(r.providers, p)
	r.mu.Unlock()
}

// GetCredentials returns cached or fresh credentials for the given host.
// Returns empty strings if no provider matches.
func (r *Registry) GetCredentials(ctx context.Context, host string) (username, password string, err error) {
	r.mu.RLock()
	// Check cache first.
	if c, ok := r.cache[host]; ok && time.Now().Before(c.expiry.Add(-5*time.Minute)) {
		r.mu.RUnlock()
		return c.username, c.password, nil
	}

	// Find matching provider.
	var provider Provider
	for _, p := range r.providers {
		if p.Matches(host) {
			provider = p
			break
		}
	}
	r.mu.RUnlock()

	if provider == nil {
		return "", "", nil
	}

	// Fetch fresh credentials.
	u, p, expiry, err := provider.GetCredentials(ctx)
	if err != nil {
		return "", "", err
	}

	// Cache them.
	r.mu.Lock()
	r.cache[host] = cachedCred{username: u, password: p, expiry: expiry}
	r.mu.Unlock()

	return u, p, nil
}

// ClearCache removes all cached credentials.
func (r *Registry) ClearCache() {
	r.mu.Lock()
	r.cache = make(map[string]cachedCred)
	r.mu.Unlock()
}

// Providers returns the names of all registered providers.
func (r *Registry) Providers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, len(r.providers))
	for i, p := range r.providers {
		names[i] = p.Name()
	}
	return names
}
