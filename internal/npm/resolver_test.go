package npm

import (
	"testing"
)

// fakeHosts builds a resolver pre-loaded with proxy hosts (no HTTP client needed).
func fakeResolver(sentinelHost string, hosts []ProxyHost) *Resolver {
	r := &Resolver{sentinelHost: sentinelHost}
	r.hosts = hosts
	return r
}

func TestLookupForHost_MatchesProvidedAddr(t *testing.T) {
	hosts := []ProxyHost{
		{ID: 1, DomainNames: []string{"radarr.example.com"}, ForwardHost: "192.168.1.57", ForwardPort: 62100, Enabled: true},
		{ID: 2, DomainNames: []string{"sonarr.example.com"}, ForwardHost: "192.168.1.61", ForwardPort: 62100, Enabled: true},
	}
	r := fakeResolver("192.168.1.57", hosts)

	// LookupForHost should match the provided host, not the resolver's sentinelHost.
	got := r.LookupForHost(62100, "192.168.1.61")
	if got == nil {
		t.Fatal("expected match for .61:62100, got nil")
	}
	if got.Domain != "sonarr.example.com" {
		t.Errorf("expected sonarr.example.com, got %s", got.Domain)
	}

	// Should return nil when no proxy host matches the given addr.
	got = r.LookupForHost(62100, "192.168.1.99")
	if got != nil {
		t.Errorf("expected nil for non-matching host, got %+v", got)
	}
}

func TestLookupForHost_EmptyAddr_MatchesAll(t *testing.T) {
	hosts := []ProxyHost{
		{ID: 1, DomainNames: []string{"app.example.com"}, ForwardHost: "10.0.0.1", ForwardPort: 8080, Enabled: true},
	}
	r := fakeResolver("", hosts)

	// Empty hostAddr should match all hosts (same as empty sentinelHost).
	got := r.LookupForHost(8080, "")
	if got == nil {
		t.Fatal("expected match with empty hostAddr, got nil")
	}
	if got.Domain != "app.example.com" {
		t.Errorf("expected app.example.com, got %s", got.Domain)
	}
}

func TestLookupForHost_HTTPS_CertificateID(t *testing.T) {
	hosts := []ProxyHost{
		{ID: 1, DomainNames: []string{"secure.example.com"}, ForwardHost: "10.0.0.1", ForwardPort: 443, Enabled: true, CertificateID: 5},
	}
	r := fakeResolver("", hosts)

	got := r.LookupForHost(443, "10.0.0.1")
	if got == nil {
		t.Fatal("expected match, got nil")
	}
	if got.URL != "https://secure.example.com" {
		t.Errorf("expected https scheme for cert host, got %s", got.URL)
	}
}

func TestAllMappingsGrouped_GroupsByHost(t *testing.T) {
	hosts := []ProxyHost{
		{ID: 1, DomainNames: []string{"radarr.example.com"}, ForwardHost: "192.168.1.57", ForwardPort: 62100, Enabled: true},
		{ID: 2, DomainNames: []string{"sonarr.example.com"}, ForwardHost: "192.168.1.61", ForwardPort: 62200, Enabled: true},
		{ID: 3, DomainNames: []string{"plex.example.com"}, ForwardHost: "192.168.1.57", ForwardPort: 32400, Enabled: true},
		{ID: 4, DomainNames: []string{"disabled.example.com"}, ForwardHost: "192.168.1.57", ForwardPort: 9999, Enabled: false},
	}
	// sentinelHost is set but AllMappingsGrouped should ignore it.
	r := fakeResolver("192.168.1.57", hosts)

	grouped := r.AllMappingsGrouped()

	// Should have two host groups.
	if len(grouped) != 2 {
		t.Fatalf("expected 2 host groups, got %d", len(grouped))
	}

	// .57 should have two mappings (disabled one excluded).
	host57 := grouped["192.168.1.57"]
	if len(host57) != 2 {
		t.Errorf("expected 2 mappings for .57, got %d", len(host57))
	}
	if m, ok := host57[62100]; !ok || m.Domain != "radarr.example.com" {
		t.Errorf("expected radarr on port 62100 for .57")
	}
	if m, ok := host57[32400]; !ok || m.Domain != "plex.example.com" {
		t.Errorf("expected plex on port 32400 for .57")
	}

	// .61 should have one mapping.
	host61 := grouped["192.168.1.61"]
	if len(host61) != 1 {
		t.Errorf("expected 1 mapping for .61, got %d", len(host61))
	}
	if m, ok := host61[62200]; !ok || m.Domain != "sonarr.example.com" {
		t.Errorf("expected sonarr on port 62200 for .61")
	}
}

func TestAllMappingsGrouped_FirstMatchWinsPerHost(t *testing.T) {
	hosts := []ProxyHost{
		{ID: 1, DomainNames: []string{"first.example.com"}, ForwardHost: "10.0.0.1", ForwardPort: 8080, Enabled: true},
		{ID: 2, DomainNames: []string{"second.example.com"}, ForwardHost: "10.0.0.1", ForwardPort: 8080, Enabled: true},
	}
	r := fakeResolver("", hosts)

	grouped := r.AllMappingsGrouped()
	host := grouped["10.0.0.1"]
	if m, ok := host[8080]; !ok || m.Domain != "first.example.com" {
		t.Errorf("expected first match to win, got %+v", host[8080])
	}
}

func TestLookup_BackwardsCompatible(t *testing.T) {
	// Verify the original Lookup method still works identically.
	hosts := []ProxyHost{
		{ID: 1, DomainNames: []string{"local.example.com"}, ForwardHost: "192.168.1.57", ForwardPort: 8080, Enabled: true},
		{ID: 2, DomainNames: []string{"remote.example.com"}, ForwardHost: "192.168.1.61", ForwardPort: 8080, Enabled: true},
	}
	r := fakeResolver("192.168.1.57", hosts)

	got := r.Lookup(8080)
	if got == nil {
		t.Fatal("expected match, got nil")
	}
	if got.Domain != "local.example.com" {
		t.Errorf("expected Lookup to match sentinelHost, got %s", got.Domain)
	}
}
