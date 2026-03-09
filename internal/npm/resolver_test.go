package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// --- httptest-based tests ---

// fakeNPMServer creates an httptest.Server that mimics the NPM API.
// It handles /api/tokens (auth) and /api/nginx/proxy-hosts (list).
func fakeNPMServer(t *testing.T, hosts []ProxyHost, authFail bool) *httptest.Server {
	t.Helper()
	var authCount atomic.Int32

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/tokens" && r.Method == http.MethodPost:
			authCount.Add(1)
			if authFail {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprintln(w, `{"error":"invalid credentials"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "test-jwt-token"})

		case r.URL.Path == "/api/nginx/proxy-hosts" && r.Method == http.MethodGet:
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(hosts)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestSyncAndLookup_ExactMatch(t *testing.T) {
	hosts := []ProxyHost{
		{ID: 1, DomainNames: []string{"app.example.com"}, ForwardHost: "192.168.1.57", ForwardPort: 8080, Enabled: true, ForwardScheme: "http"},
		{ID: 2, DomainNames: []string{"api.example.com"}, ForwardHost: "192.168.1.57", ForwardPort: 9090, Enabled: true, ForwardScheme: "http"},
	}

	srv := fakeNPMServer(t, hosts, false)
	defer srv.Close()

	client := NewClient(srv.URL, "admin@example.com", "password123")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := NewResolver(client, "192.168.1.57", log)

	err := resolver.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	// Exact match on port 8080.
	got := resolver.Lookup(8080)
	if got == nil {
		t.Fatal("expected match for port 8080, got nil")
	}
	if got.Domain != "app.example.com" {
		t.Errorf("Domain = %q, want app.example.com", got.Domain)
	}
	if got.URL != "http://app.example.com" {
		t.Errorf("URL = %q, want http://app.example.com", got.URL)
	}
	if got.ProxyHostID != 1 {
		t.Errorf("ProxyHostID = %d, want 1", got.ProxyHostID)
	}
}

func TestSyncAndLookup_NoMatch(t *testing.T) {
	hosts := []ProxyHost{
		{ID: 1, DomainNames: []string{"app.example.com"}, ForwardHost: "192.168.1.57", ForwardPort: 8080, Enabled: true},
	}

	srv := fakeNPMServer(t, hosts, false)
	defer srv.Close()

	client := NewClient(srv.URL, "admin@example.com", "password123")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := NewResolver(client, "192.168.1.57", log)

	_ = resolver.Sync(context.Background())

	// Port 9999 has no proxy host.
	got := resolver.Lookup(9999)
	if got != nil {
		t.Errorf("expected nil for unmatched port, got %+v", got)
	}
}

func TestSyncAndLookup_DisabledHostSkipped(t *testing.T) {
	hosts := []ProxyHost{
		{ID: 1, DomainNames: []string{"disabled.example.com"}, ForwardHost: "192.168.1.57", ForwardPort: 8080, Enabled: false},
		{ID: 2, DomainNames: []string{"enabled.example.com"}, ForwardHost: "192.168.1.57", ForwardPort: 8080, Enabled: true},
	}

	srv := fakeNPMServer(t, hosts, false)
	defer srv.Close()

	client := NewClient(srv.URL, "admin@example.com", "password123")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := NewResolver(client, "192.168.1.57", log)

	_ = resolver.Sync(context.Background())

	got := resolver.Lookup(8080)
	if got == nil {
		t.Fatal("expected match, got nil")
	}
	// Should match the enabled one, not the disabled one.
	if got.Domain != "enabled.example.com" {
		t.Errorf("Domain = %q, want enabled.example.com (disabled should be skipped)", got.Domain)
	}
}

func TestSync_AuthFailure(t *testing.T) {
	srv := fakeNPMServer(t, nil, true)
	defer srv.Close()

	client := NewClient(srv.URL, "wrong@example.com", "badpassword")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := NewResolver(client, "192.168.1.57", log)

	err := resolver.Sync(context.Background())
	if err == nil {
		t.Fatal("expected auth error, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, want to contain '403'", err.Error())
	}

	// LastError should reflect the auth failure.
	if resolver.LastError() == nil {
		t.Error("LastError() should be non-nil after failed sync")
	}

	// Lookup should return nil (no cached hosts).
	got := resolver.Lookup(8080)
	if got != nil {
		t.Errorf("expected nil lookup after auth failure, got %+v", got)
	}
}

func TestAllMappings_ReturnsMatchedPorts(t *testing.T) {
	hosts := []ProxyHost{
		{ID: 1, DomainNames: []string{"app1.example.com"}, ForwardHost: "192.168.1.57", ForwardPort: 8080, Enabled: true, ForwardScheme: "http"},
		{ID: 2, DomainNames: []string{"app2.example.com"}, ForwardHost: "192.168.1.57", ForwardPort: 9090, Enabled: true, ForwardScheme: "https"},
		{ID: 3, DomainNames: []string{"other.example.com"}, ForwardHost: "10.0.0.1", ForwardPort: 3000, Enabled: true},
	}
	// sentinelHost filters to .57 only.
	r := fakeResolver("192.168.1.57", hosts)

	mappings := r.AllMappings()

	if len(mappings) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(mappings))
	}

	m1, ok := mappings[8080]
	if !ok {
		t.Fatal("expected mapping for port 8080")
	}
	if m1.Domain != "app1.example.com" {
		t.Errorf("port 8080 domain = %q, want app1.example.com", m1.Domain)
	}
	if m1.URL != "http://app1.example.com" {
		t.Errorf("port 8080 URL = %q, want http://app1.example.com", m1.URL)
	}

	m2, ok := mappings[9090]
	if !ok {
		t.Fatal("expected mapping for port 9090")
	}
	if m2.URL != "https://app2.example.com" {
		t.Errorf("port 9090 URL = %q, want https://app2.example.com", m2.URL)
	}

	// Port 3000 should not be present (different host).
	if _, ok := mappings[3000]; ok {
		t.Error("port 3000 should not be mapped (different forward host)")
	}
}

func TestAllMappingsGrouped_ViaHTTPTest(t *testing.T) {
	hosts := []ProxyHost{
		{ID: 1, DomainNames: []string{"app.host-a.com"}, ForwardHost: "host-a", ForwardPort: 8080, Enabled: true},
		{ID: 2, DomainNames: []string{"app.host-b.com"}, ForwardHost: "host-b", ForwardPort: 9090, Enabled: true},
		{ID: 3, DomainNames: []string{"api.host-a.com"}, ForwardHost: "host-a", ForwardPort: 3000, Enabled: true},
	}

	srv := fakeNPMServer(t, hosts, false)
	defer srv.Close()

	client := NewClient(srv.URL, "admin@example.com", "password123")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := NewResolver(client, "", log) // empty sentinelHost

	_ = resolver.Sync(context.Background())

	grouped := resolver.AllMappingsGrouped()

	if len(grouped) != 2 {
		t.Fatalf("expected 2 host groups, got %d", len(grouped))
	}

	hostA := grouped["host-a"]
	if len(hostA) != 2 {
		t.Errorf("host-a: expected 2 mappings, got %d", len(hostA))
	}
	if m, ok := hostA[8080]; !ok || m.Domain != "app.host-a.com" {
		t.Errorf("host-a:8080 = %+v, want app.host-a.com", hostA[8080])
	}
	if m, ok := hostA[3000]; !ok || m.Domain != "api.host-a.com" {
		t.Errorf("host-a:3000 = %+v, want api.host-a.com", hostA[3000])
	}

	hostB := grouped["host-b"]
	if len(hostB) != 1 {
		t.Errorf("host-b: expected 1 mapping, got %d", len(hostB))
	}
}

func TestLookup_EmptyDomainNamesSkipped(t *testing.T) {
	hosts := []ProxyHost{
		{ID: 1, DomainNames: []string{}, ForwardHost: "192.168.1.57", ForwardPort: 8080, Enabled: true},
		{ID: 2, DomainNames: []string{"fallback.example.com"}, ForwardHost: "192.168.1.57", ForwardPort: 8080, Enabled: true},
	}
	r := fakeResolver("192.168.1.57", hosts)

	got := r.Lookup(8080)
	if got == nil {
		t.Fatal("expected match, got nil")
	}
	// Should skip the empty DomainNames entry and match the second one.
	if got.Domain != "fallback.example.com" {
		t.Errorf("Domain = %q, want fallback.example.com", got.Domain)
	}
}

func TestFlexBool_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"bool true", `true`, true},
		{"bool false", `false`, false},
		{"int 1", `1`, true},
		{"int 0", `0`, false},
		{"int 42", `42`, true},
		{"string (invalid)", `"yes"`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b flexBool
			err := json.Unmarshal([]byte(tt.input), &b)
			if err != nil {
				t.Fatalf("UnmarshalJSON(%q) error = %v", tt.input, err)
			}
			if bool(b) != tt.want {
				t.Errorf("UnmarshalJSON(%q) = %v, want %v", tt.input, bool(b), tt.want)
			}
		})
	}
}

func TestLastSync_ZeroBeforeSync(t *testing.T) {
	r := fakeResolver("", nil)
	if !r.LastSync().IsZero() {
		t.Errorf("LastSync() should be zero before any sync, got %v", r.LastSync())
	}
}

func TestLastError_NilBeforeSync(t *testing.T) {
	r := fakeResolver("", nil)
	if r.LastError() != nil {
		t.Errorf("LastError() should be nil before any sync, got %v", r.LastError())
	}
}
