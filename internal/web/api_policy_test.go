package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// ---------------------------------------------------------------------------
// Mock: ContainerLister
// ---------------------------------------------------------------------------

type mockContainerLister struct {
	containers []ContainerSummary
}

func (m *mockContainerLister) ListContainers(_ context.Context) ([]ContainerSummary, error) {
	return nil, nil
}

func (m *mockContainerLister) ListAllContainers(_ context.Context) ([]ContainerSummary, error) {
	return m.containers, nil
}

func (m *mockContainerLister) InspectContainer(_ context.Context, _ string) (ContainerInspect, error) {
	return ContainerInspect{}, nil
}

// ---------------------------------------------------------------------------
// Mock: PolicyStore
// ---------------------------------------------------------------------------

type mockPolicyStore struct {
	overrides map[string]string
}

func newMockPolicyStore() *mockPolicyStore {
	return &mockPolicyStore{overrides: make(map[string]string)}
}

func (m *mockPolicyStore) GetPolicyOverride(name string) (string, bool) {
	v, ok := m.overrides[name]
	return v, ok
}

func (m *mockPolicyStore) SetPolicyOverride(name, policy string) error {
	m.overrides[name] = policy
	return nil
}

func (m *mockPolicyStore) DeletePolicyOverride(name string) error {
	delete(m.overrides, name)
	return nil
}

func (m *mockPolicyStore) AllPolicyOverrides() map[string]string {
	cp := make(map[string]string, len(m.overrides))
	for k, v := range m.overrides {
		cp[k] = v
	}
	return cp
}

// ---------------------------------------------------------------------------
// Mock: SwarmProvider
// ---------------------------------------------------------------------------

type mockSwarmProvider struct {
	swarmMode bool
	services  []ServiceDetail
}

func (m *mockSwarmProvider) IsSwarmMode() bool { return m.swarmMode }

func (m *mockSwarmProvider) ListServices(_ context.Context) ([]ServiceSummary, error) {
	out := make([]ServiceSummary, len(m.services))
	for i, s := range m.services {
		out[i] = s.ServiceSummary
	}
	return out, nil
}

func (m *mockSwarmProvider) ListServiceDetail(_ context.Context) ([]ServiceDetail, error) {
	return m.services, nil
}

func (m *mockSwarmProvider) UpdateService(_ context.Context, _, _, _ string) error { return nil }
func (m *mockSwarmProvider) RollbackService(_ context.Context, _, _ string) error  { return nil }
func (m *mockSwarmProvider) ScaleService(_ context.Context, _ string, _ uint64) error {
	return nil
}

// ---------------------------------------------------------------------------
// Mock: ClusterProvider with containers
// ---------------------------------------------------------------------------

// mockClusterProviderWithContainers implements ClusterProvider and returns
// canned remote containers (unlike mockClusterProvider which returns nil).
type mockClusterProviderWithContainers struct {
	hosts      []ClusterHost
	connected  []string
	containers []RemoteContainer
}

func (m *mockClusterProviderWithContainers) AllHosts() []ClusterHost { return m.hosts }

func (m *mockClusterProviderWithContainers) GetHost(id string) (ClusterHost, bool) {
	for _, h := range m.hosts {
		if h.ID == id {
			return h, true
		}
	}
	return ClusterHost{}, false
}

func (m *mockClusterProviderWithContainers) ConnectedHosts() []string { return m.connected }

func (m *mockClusterProviderWithContainers) GenerateEnrollToken() (string, string, error) {
	return "tok", "id", nil
}

func (m *mockClusterProviderWithContainers) RemoveHost(_ string) error { return nil }
func (m *mockClusterProviderWithContainers) RevokeHost(_ string) error { return nil }
func (m *mockClusterProviderWithContainers) DrainHost(_ string) error  { return nil }

func (m *mockClusterProviderWithContainers) UpdateRemoteContainer(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (m *mockClusterProviderWithContainers) RemoteContainerAction(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockClusterProviderWithContainers) AllHostContainers() []RemoteContainer {
	return m.containers
}

// ---------------------------------------------------------------------------
// Test server helpers
// ---------------------------------------------------------------------------

// newPolicyTestServer builds a Server wired for bulk policy tests.
// All three container sources (Docker, Swarm, Cluster) can be optionally supplied.
func newPolicyTestServer(
	docker *mockContainerLister,
	policy *mockPolicyStore,
	swarm *mockSwarmProvider,
	clusterContainers []RemoteContainer,
) *Server {
	cc := NewClusterController()
	if clusterContainers != nil {
		cc.SetProvider(&mockClusterProviderWithContainers{
			hosts:      []ClusterHost{{ID: "h1", Name: "remote-host"}},
			connected:  []string{"h1"},
			containers: clusterContainers,
		})
	}

	var swarmProv SwarmProvider
	if swarm != nil {
		swarmProv = swarm
	}

	return &Server{
		deps: Dependencies{
			Docker:   docker,
			Policy:   policy,
			Swarm:    swarmProv,
			Cluster:  cc,
			EventBus: events.New(),
			Log:      slog.Default(),
		},
	}
}

// doBulkPolicy makes a POST to apiBulkPolicy and returns the recorder.
func doBulkPolicy(srv *Server, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/bulk/policy", strings.NewReader(body))
	srv.apiBulkPolicy(w, r)
	return w
}

// decodeMap is a convenience wrapper for JSON-decoding a response body.
func decodeMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, w.Body.String())
	}
	return m
}

// ---------------------------------------------------------------------------
// allContainerLabels tests
// ---------------------------------------------------------------------------

func TestAllContainerLabels_LocalOnly(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "c1", Names: []string{"/nginx"}, Labels: map[string]string{"sentinel.policy": "auto"}},
			{ID: "c2", Names: []string{"/redis"}, Labels: map[string]string{"env": "prod"}},
		},
	}
	srv := newPolicyTestServer(docker, newMockPolicyStore(), nil, nil)

	labels := srv.allContainerLabels(context.Background())

	if len(labels) != 2 {
		t.Fatalf("got %d entries, want 2", len(labels))
	}
	if labels["nginx"]["sentinel.policy"] != "auto" {
		t.Errorf("nginx sentinel.policy = %q, want %q", labels["nginx"]["sentinel.policy"], "auto")
	}
	if labels["redis"]["env"] != "prod" {
		t.Errorf("redis env = %q, want %q", labels["redis"]["env"], "prod")
	}
}

func TestAllContainerLabels_WithSwarm(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "c1", Names: []string{"/nginx"}, Labels: map[string]string{"source": "local"}},
		},
	}
	swarm := &mockSwarmProvider{
		swarmMode: true,
		services: []ServiceDetail{
			{
				ServiceSummary: ServiceSummary{
					ID:     "svc1",
					Name:   "nginx", // same name as local -- local should win
					Labels: map[string]string{"source": "swarm"},
				},
			},
			{
				ServiceSummary: ServiceSummary{
					ID:     "svc2",
					Name:   "traefik",
					Labels: map[string]string{"sentinel.policy": "pinned"},
				},
			},
		},
	}
	srv := newPolicyTestServer(docker, newMockPolicyStore(), swarm, nil)

	labels := srv.allContainerLabels(context.Background())

	// nginx should come from local (higher priority).
	if labels["nginx"]["source"] != "local" {
		t.Errorf("nginx source = %q, want %q (local takes precedence)", labels["nginx"]["source"], "local")
	}
	// traefik only exists in Swarm.
	if labels["traefik"]["sentinel.policy"] != "pinned" {
		t.Errorf("traefik sentinel.policy = %q, want %q", labels["traefik"]["sentinel.policy"], "pinned")
	}
}

func TestAllContainerLabels_WithCluster(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "c1", Names: []string{"/nginx"}, Labels: map[string]string{"source": "local"}},
		},
	}
	remotes := []RemoteContainer{
		{Name: "postgres", Image: "postgres:16", HostID: "h1", Labels: map[string]string{"sentinel.policy": "manual"}},
		{Name: "nginx", Image: "nginx:latest", HostID: "h1", Labels: map[string]string{"source": "cluster"}},
	}
	srv := newPolicyTestServer(docker, newMockPolicyStore(), nil, remotes)

	labels := srv.allContainerLabels(context.Background())

	// nginx: local takes precedence over cluster.
	if labels["nginx"]["source"] != "local" {
		t.Errorf("nginx source = %q, want %q (local takes precedence)", labels["nginx"]["source"], "local")
	}
	// postgres: only exists on cluster.
	if labels["postgres"]["sentinel.policy"] != "manual" {
		t.Errorf("postgres sentinel.policy = %q, want %q", labels["postgres"]["sentinel.policy"], "manual")
	}
}

func TestAllContainerLabels_AllSources(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "c1", Names: []string{"/nginx"}, Labels: map[string]string{"source": "local"}},
		},
	}
	swarm := &mockSwarmProvider{
		swarmMode: true,
		services: []ServiceDetail{
			{
				ServiceSummary: ServiceSummary{
					ID:     "svc1",
					Name:   "nginx", // collides with local
					Labels: map[string]string{"source": "swarm"},
				},
			},
			{
				ServiceSummary: ServiceSummary{
					ID:     "svc2",
					Name:   "traefik", // collides with cluster below
					Labels: map[string]string{"source": "swarm"},
				},
			},
		},
	}
	remotes := []RemoteContainer{
		{Name: "traefik", Image: "traefik:v3", HostID: "h1", Labels: map[string]string{"source": "cluster"}},
		{Name: "postgres", Image: "postgres:16", HostID: "h1", Labels: map[string]string{"source": "cluster"}},
	}
	srv := newPolicyTestServer(docker, newMockPolicyStore(), swarm, remotes)

	labels := srv.allContainerLabels(context.Background())

	if len(labels) != 3 {
		t.Fatalf("got %d entries, want 3 (nginx, traefik, postgres)", len(labels))
	}

	// Precedence: local > Swarm > cluster.
	if labels["nginx"]["source"] != "local" {
		t.Errorf("nginx source = %q, want %q", labels["nginx"]["source"], "local")
	}
	if labels["traefik"]["source"] != "swarm" {
		t.Errorf("traefik source = %q, want %q (Swarm > cluster)", labels["traefik"]["source"], "swarm")
	}
	if labels["postgres"]["source"] != "cluster" {
		t.Errorf("postgres source = %q, want %q", labels["postgres"]["source"], "cluster")
	}
}

// ---------------------------------------------------------------------------
// apiBulkPolicy tests
// ---------------------------------------------------------------------------

func TestBulkPolicy_LocalContainers(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "c1", Names: []string{"/nginx"}, Labels: map[string]string{"sentinel.policy": "manual"}},
			{ID: "c2", Names: []string{"/redis"}, Labels: map[string]string{"sentinel.policy": "manual"}},
		},
	}
	policy := newMockPolicyStore()
	srv := newPolicyTestServer(docker, policy, nil, nil)

	// Preview first.
	body := `{"containers":["nginx","redis"],"policy":"auto","confirm":false}`
	w := doBulkPolicy(srv, body)

	if w.Code != http.StatusOK {
		t.Fatalf("preview: status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	result := decodeMap(t, w)
	if result["mode"] != "preview" {
		t.Errorf("mode = %v, want %q", result["mode"], "preview")
	}
	changes, ok := result["changes"].([]any)
	if !ok {
		t.Fatalf("changes is not an array: %T", result["changes"])
	}
	if len(changes) != 2 {
		t.Fatalf("changes length = %d, want 2", len(changes))
	}

	// Now confirm.
	body = `{"containers":["nginx","redis"],"policy":"auto","confirm":true}`
	w = doBulkPolicy(srv, body)

	if w.Code != http.StatusOK {
		t.Fatalf("confirm: status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	result = decodeMap(t, w)
	if result["mode"] != "executed" {
		t.Errorf("mode = %v, want %q", result["mode"], "executed")
	}
	if result["applied"] != float64(2) {
		t.Errorf("applied = %v, want 2", result["applied"])
	}

	// Verify policy store was actually updated.
	if p, ok := policy.GetPolicyOverride("nginx"); !ok || p != "auto" {
		t.Errorf("nginx policy = (%q, %v), want (\"auto\", true)", p, ok)
	}
	if p, ok := policy.GetPolicyOverride("redis"); !ok || p != "auto" {
		t.Errorf("redis policy = (%q, %v), want (\"auto\", true)", p, ok)
	}
}

func TestBulkPolicy_RemoteContainers(t *testing.T) {
	// No local containers -- everything comes from the cluster.
	docker := &mockContainerLister{}
	policy := newMockPolicyStore()
	remotes := []RemoteContainer{
		{Name: "postgres", Image: "postgres:16", HostID: "h1", Labels: map[string]string{"sentinel.policy": "manual"}},
		{Name: "mongo", Image: "mongo:7", HostID: "h1", Labels: map[string]string{}},
	}
	srv := newPolicyTestServer(docker, policy, nil, remotes)

	// Preview.
	body := `{"containers":["postgres","mongo"],"policy":"pinned","confirm":false}`
	w := doBulkPolicy(srv, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	result := decodeMap(t, w)
	changes := result["changes"].([]any)
	if len(changes) != 2 {
		t.Fatalf("changes = %d, want 2", len(changes))
	}

	// Confirm.
	body = `{"containers":["postgres","mongo"],"policy":"pinned","confirm":true}`
	w = doBulkPolicy(srv, body)

	result = decodeMap(t, w)
	if result["applied"] != float64(2) {
		t.Errorf("applied = %v, want 2", result["applied"])
	}

	// Verify overrides were written.
	if p, _ := policy.GetPolicyOverride("postgres"); p != "pinned" {
		t.Errorf("postgres policy = %q, want %q", p, "pinned")
	}
	if p, _ := policy.GetPolicyOverride("mongo"); p != "pinned" {
		t.Errorf("mongo policy = %q, want %q", p, "pinned")
	}
}

func TestBulkPolicy_AlreadyHasPolicy(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "c1", Names: []string{"/nginx"}, Labels: map[string]string{"sentinel.policy": "auto"}},
			{ID: "c2", Names: []string{"/redis"}, Labels: map[string]string{"sentinel.policy": "manual"}},
		},
	}
	srv := newPolicyTestServer(docker, newMockPolicyStore(), nil, nil)

	// Both containers get asked for "auto", but nginx already has it.
	body := `{"containers":["nginx","redis"],"policy":"auto","confirm":false}`
	w := doBulkPolicy(srv, body)

	result := decodeMap(t, w)
	changes := result["changes"].([]any)
	unchanged := result["unchanged"].([]any)

	if len(changes) != 1 {
		t.Errorf("changes length = %d, want 1 (only redis)", len(changes))
	}
	if len(unchanged) != 1 {
		t.Errorf("unchanged length = %d, want 1 (nginx already auto)", len(unchanged))
	}

	// Verify the unchanged entry is nginx.
	entry := unchanged[0].(map[string]any)
	if entry["name"] != "nginx" {
		t.Errorf("unchanged name = %v, want %q", entry["name"], "nginx")
	}
}

func TestBulkPolicy_SelfProtected(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "c1", Names: []string{"/sentinel"}, Labels: map[string]string{
				"sentinel.self":   "true",
				"sentinel.policy": "auto",
			}},
			{ID: "c2", Names: []string{"/nginx"}, Labels: map[string]string{
				"sentinel.policy": "manual",
			}},
		},
	}
	srv := newPolicyTestServer(docker, newMockPolicyStore(), nil, nil)

	body := `{"containers":["sentinel","nginx"],"policy":"pinned","confirm":false}`
	w := doBulkPolicy(srv, body)

	result := decodeMap(t, w)
	blocked := result["blocked"].([]any)
	changes := result["changes"].([]any)

	if len(blocked) != 1 {
		t.Fatalf("blocked length = %d, want 1", len(blocked))
	}
	entry := blocked[0].(map[string]any)
	if entry["name"] != "sentinel" {
		t.Errorf("blocked name = %v, want %q", entry["name"], "sentinel")
	}
	if entry["reason"] != "self-protected" {
		t.Errorf("blocked reason = %v, want %q", entry["reason"], "self-protected")
	}

	if len(changes) != 1 {
		t.Errorf("changes length = %d, want 1 (nginx)", len(changes))
	}
}

func TestBulkPolicy_OverrideExists(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			// Label says "manual", but there is a DB override to "auto".
			{ID: "c1", Names: []string{"/nginx"}, Labels: map[string]string{"sentinel.policy": "manual"}},
			{ID: "c2", Names: []string{"/redis"}, Labels: map[string]string{"sentinel.policy": "manual"}},
		},
	}
	policy := newMockPolicyStore()
	policy.overrides["nginx"] = "auto" // DB override

	srv := newPolicyTestServer(docker, policy, nil, nil)

	// Request "auto" for both. nginx already has "auto" via override, so it is unchanged.
	body := `{"containers":["nginx","redis"],"policy":"auto","confirm":false}`
	w := doBulkPolicy(srv, body)

	result := decodeMap(t, w)
	changes := result["changes"].([]any)
	unchanged := result["unchanged"].([]any)

	if len(unchanged) != 1 {
		t.Fatalf("unchanged length = %d, want 1 (nginx has override)", len(unchanged))
	}
	entry := unchanged[0].(map[string]any)
	if entry["name"] != "nginx" {
		t.Errorf("unchanged name = %v, want %q", entry["name"], "nginx")
	}

	if len(changes) != 1 {
		t.Fatalf("changes length = %d, want 1 (redis)", len(changes))
	}
	ch := changes[0].(map[string]any)
	if ch["name"] != "redis" {
		t.Errorf("change name = %v, want %q", ch["name"], "redis")
	}
	if ch["from"] != "manual" {
		t.Errorf("change from = %v, want %q", ch["from"], "manual")
	}
	if ch["to"] != "auto" {
		t.Errorf("change to = %v, want %q", ch["to"], "auto")
	}
}

func TestBulkPolicy_InvalidPolicy(t *testing.T) {
	docker := &mockContainerLister{}
	srv := newPolicyTestServer(docker, newMockPolicyStore(), nil, nil)

	body := `{"containers":["nginx"],"policy":"yolo"}`
	w := doBulkPolicy(srv, body)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestBulkPolicy_EmptyContainers(t *testing.T) {
	docker := &mockContainerLister{}
	srv := newPolicyTestServer(docker, newMockPolicyStore(), nil, nil)

	body := `{"containers":[],"policy":"auto"}`
	w := doBulkPolicy(srv, body)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestBulkPolicy_InvalidJSON(t *testing.T) {
	docker := &mockContainerLister{}
	srv := newPolicyTestServer(docker, newMockPolicyStore(), nil, nil)

	w := doBulkPolicy(srv, "{broken")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
