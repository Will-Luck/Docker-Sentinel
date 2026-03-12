package npm

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// Resolver maintains an in-memory cache of NPM proxy hosts and resolves
// container ports to domain URLs.
type Resolver struct {
	client     *Client
	localAddrs map[string]bool // set of local IPs/hostnames (lowercase) for filtering
	log        *slog.Logger

	mu       sync.RWMutex
	hosts    []ProxyHost
	lastSync time.Time
	syncErr  error
}

// NewResolver creates a resolver that matches proxy hosts whose ForwardHost
// is in the localAddrs set. Pass nil or empty to match all hosts (unfiltered).
func NewResolver(client *Client, localAddrs map[string]bool, log *slog.Logger) *Resolver {
	return &Resolver{
		client:     client,
		localAddrs: localAddrs,
		log:        log,
	}
}

// DetectLocalAddrs builds a set of local IP addresses by querying network
// interfaces. The machine hostname and "localhost" are included. Any extra
// values (e.g. from SENTINEL_HOST) are added to the set. Empty strings in
// extra are ignored.
func DetectLocalAddrs(extra ...string) map[string]bool {
	addrs := make(map[string]bool)

	// Network interfaces — the primary source of truth.
	if ifaces, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifaces {
			ip, _, _ := net.ParseCIDR(a.String())
			if ip != nil {
				addrs[strings.ToLower(ip.String())] = true
			}
		}
	}

	// Machine hostname — NPM ForwardHost might use it.
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		addrs[strings.ToLower(hostname)] = true
	}

	addrs["localhost"] = true

	// Docker host detection: host.docker.internal resolves to the Docker
	// host IP on Docker Desktop and on containers started with
	// --add-host=host.docker.internal:host-gateway.
	if ips, err := net.LookupHost("host.docker.internal"); err == nil {
		for _, ip := range ips {
			addrs[strings.ToLower(ip)] = true
		}
	}

	// Explicit overrides (SENTINEL_HOST, etc.).
	for _, h := range extra {
		if h != "" {
			addrs[strings.ToLower(h)] = true
		}
	}

	return addrs
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
		if len(r.localAddrs) > 0 && !r.localAddrs[strings.ToLower(h.ForwardHost)] {
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

// AllMappings returns all matched port-to-URL mappings for local addresses.
// When localAddrs is empty, all enabled proxy hosts are returned.
func (r *Resolver) AllMappings() map[uint16]ResolvedURL {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[uint16]ResolvedURL)
	for _, h := range r.hosts {
		domain := bestDomain(h.DomainNames)
		if !bool(h.Enabled) || domain == "" {
			continue
		}
		if len(r.localAddrs) > 0 && !r.localAddrs[strings.ToLower(h.ForwardHost)] {
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
// the provided hostAddr instead of the resolver's local address set. When
// hostAddr is empty, all hosts match (no filtering).
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
