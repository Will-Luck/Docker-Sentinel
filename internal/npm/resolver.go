package npm

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"
)

// Resolver maintains an in-memory cache of NPM proxy hosts and resolves
// container ports to domain URLs.
type Resolver struct {
	client       *Client
	sentinelHost string // SENTINEL_HOST value for matching forward_host
	log          *slog.Logger

	mu       sync.RWMutex
	hosts    []ProxyHost
	lastSync time.Time
	syncErr  error
}

// NewResolver creates a resolver that matches proxy hosts against sentinelHost.
func NewResolver(client *Client, sentinelHost string, log *slog.Logger) *Resolver {
	return &Resolver{
		client:       client,
		sentinelHost: sentinelHost,
		log:          log,
	}
}

// Run syncs proxy hosts immediately then every 5 minutes until ctx is cancelled.
// Errors are logged but don't stop the loop; stale cache is acceptable.
func (r *Resolver) Run(ctx context.Context) {
	_ = r.sync(ctx)

	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_ = r.sync(ctx)
		}
	}
}

// Sync fetches proxy hosts from NPM and updates the cache. Exported for
// on-demand refresh (e.g. after settings change).
func (r *Resolver) Sync(ctx context.Context) error {
	return r.sync(ctx)
}

func (r *Resolver) sync(ctx context.Context) error {
	hosts, err := r.client.ListProxyHosts(ctx)

	r.mu.Lock()
	defer r.mu.Unlock()

	if err != nil {
		r.syncErr = err
		r.log.Warn("npm proxy host sync failed", "error", err)
		return err
	}

	r.hosts = hosts
	r.lastSync = time.Now()
	r.syncErr = nil
	r.log.Debug("npm proxy hosts synced", "count", len(hosts))
	return nil
}

// Lookup resolves a host port to its NPM proxy URL. Returns nil if no match.
func (r *Resolver) Lookup(hostPort uint16) *ResolvedURL {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, h := range r.hosts {
		domain := bestDomain(h.DomainNames)
		if !bool(h.Enabled) || domain == "" {
			continue
		}
		if r.sentinelHost != "" && !strings.EqualFold(h.ForwardHost, r.sentinelHost) {
			continue
		}
		if h.ForwardPort < 0 || h.ForwardPort > math.MaxUint16 || uint16(h.ForwardPort) != hostPort {
			continue
		}

		scheme := h.ForwardScheme
		if h.CertificateID > 0 {
			scheme = "https"
		}
		if scheme == "" {
			scheme = "http"
		}

		return &ResolvedURL{
			URL:         fmt.Sprintf("%s://%s", scheme, domain),
			Domain:      domain,
			ProxyHostID: h.ID,
		}
	}
	return nil
}

// AllMappings returns all matched port-to-URL mappings for the sentinel host.
// When sentinelHost is empty, all enabled proxy hosts are returned.
func (r *Resolver) AllMappings() map[uint16]ResolvedURL {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[uint16]ResolvedURL)
	for _, h := range r.hosts {
		domain := bestDomain(h.DomainNames)
		if !bool(h.Enabled) || domain == "" {
			continue
		}
		if r.sentinelHost != "" && !strings.EqualFold(h.ForwardHost, r.sentinelHost) {
			continue
		}

		if h.ForwardPort < 0 || h.ForwardPort > math.MaxUint16 {
			continue
		}
		port := uint16(h.ForwardPort)
		if _, exists := out[port]; exists {
			continue // first match wins, same as Lookup
		}

		scheme := h.ForwardScheme
		if h.CertificateID > 0 {
			scheme = "https"
		}
		if scheme == "" {
			scheme = "http"
		}

		out[port] = ResolvedURL{
			URL:         fmt.Sprintf("%s://%s", scheme, domain),
			Domain:      domain,
			ProxyHostID: h.ID,
		}
	}
	return out
}

// LookupForHost resolves a host port to its NPM proxy URL, matching against
// the provided hostAddr instead of the resolver's sentinelHost. When hostAddr
// is empty, all hosts match (same as empty sentinelHost behaviour).
func (r *Resolver) LookupForHost(hostPort uint16, hostAddr string) *ResolvedURL {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, h := range r.hosts {
		domain := bestDomain(h.DomainNames)
		if !bool(h.Enabled) || domain == "" {
			continue
		}
		if hostAddr != "" && !strings.EqualFold(h.ForwardHost, hostAddr) {
			continue
		}
		if h.ForwardPort < 0 || h.ForwardPort > math.MaxUint16 || uint16(h.ForwardPort) != hostPort {
			continue
		}

		scheme := h.ForwardScheme
		if h.CertificateID > 0 {
			scheme = "https"
		}
		if scheme == "" {
			scheme = "http"
		}

		return &ResolvedURL{
			URL:         fmt.Sprintf("%s://%s", scheme, domain),
			Domain:      domain,
			ProxyHostID: h.ID,
		}
	}
	return nil
}

// AllMappingsGrouped returns all enabled proxy hosts grouped by ForwardHost.
// Outer key is the IP/hostname, inner map is port -> ResolvedURL.
// No sentinelHost filtering is applied -- returns mappings for all hosts.
func (r *Resolver) AllMappingsGrouped() map[string]map[uint16]ResolvedURL {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string]map[uint16]ResolvedURL)
	for _, h := range r.hosts {
		domain := bestDomain(h.DomainNames)
		if !bool(h.Enabled) || domain == "" {
			continue
		}
		if h.ForwardPort < 0 || h.ForwardPort > math.MaxUint16 {
			continue
		}
		port := uint16(h.ForwardPort)

		hostMap, ok := out[h.ForwardHost]
		if !ok {
			hostMap = make(map[uint16]ResolvedURL)
			out[h.ForwardHost] = hostMap
		}
		if _, exists := hostMap[port]; exists {
			continue // first match wins per host
		}

		scheme := h.ForwardScheme
		if h.CertificateID > 0 {
			scheme = "https"
		}
		if scheme == "" {
			scheme = "http"
		}

		hostMap[port] = ResolvedURL{
			URL:         fmt.Sprintf("%s://%s", scheme, domain),
			Domain:      domain,
			ProxyHostID: h.ID,
		}
	}
	return out
}

// bestDomain picks the first non-wildcard domain from a proxy host's domain
// list. Wildcard entries like "*.s3.garage.example.com" are valid for NPM
// routing but produce broken URLs. Returns "" if all domains are wildcards.
func bestDomain(domains []string) string {
	for _, d := range domains {
		if !strings.HasPrefix(d, "*") {
			return d
		}
	}
	return ""
}

// LastSync returns the time of the last successful sync.
func (r *Resolver) LastSync() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastSync
}

// LastError returns the error from the most recent sync attempt, or nil.
func (r *Resolver) LastError() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.syncErr
}
