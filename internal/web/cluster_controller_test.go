package web

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// mockClusterProvider implements ClusterProvider with canned responses.
type mockClusterProvider struct {
	hosts     []ClusterHost
	connected []string
}

func (m *mockClusterProvider) AllHosts() []ClusterHost {
	return m.hosts
}

func (m *mockClusterProvider) GetHost(id string) (ClusterHost, bool) {
	for _, h := range m.hosts {
		if h.ID == id {
			return h, true
		}
	}
	return ClusterHost{}, false
}

func (m *mockClusterProvider) ConnectedHosts() []string {
	return m.connected
}

func (m *mockClusterProvider) GenerateEnrollToken() (string, string, error) {
	return "tok-abc", "id-123", nil
}

func (m *mockClusterProvider) RemoveHost(id string) error {
	return nil
}

func (m *mockClusterProvider) RevokeHost(id string) error {
	return nil
}

func (m *mockClusterProvider) PauseHost(id string) error {
	return nil
}

func (m *mockClusterProvider) UpdateRemoteContainer(_ context.Context, hostID, containerName, targetImage, targetDigest string) error {
	return nil
}

func (m *mockClusterProvider) RemoteContainerAction(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockClusterProvider) RemoteContainerLogs(_ context.Context, _, _ string, _ int) (string, error) {
	return "", nil
}

func (m *mockClusterProvider) RollbackRemoteContainer(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockClusterProvider) AllHostContainers() []RemoteContainer {
	return nil // not needed for existing tests
}

func TestNewClusterControllerStartsDisabled(t *testing.T) {
	cc := NewClusterController()
	if cc.Enabled() {
		t.Error("expected new ClusterController to be disabled")
	}
}

func TestEnabledReturnsFalseWithNoProvider(t *testing.T) {
	cc := NewClusterController()
	if cc.Enabled() {
		t.Error("Enabled() should return false with no provider")
	}

	// All methods should return zero values.
	if hosts := cc.AllHosts(); hosts != nil {
		t.Errorf("AllHosts() = %v, want nil", hosts)
	}
	if _, found := cc.GetHost("any"); found {
		t.Error("GetHost() should return false when disabled")
	}
	if ids := cc.ConnectedHosts(); ids != nil {
		t.Errorf("ConnectedHosts() = %v, want nil", ids)
	}
	if _, _, err := cc.GenerateEnrollToken(); err == nil {
		t.Error("GenerateEnrollToken() should return error when disabled")
	}
	if err := cc.RemoveHost("any"); err == nil {
		t.Error("RemoveHost() should return error when disabled")
	}
	if err := cc.RevokeHost("any"); err == nil {
		t.Error("RevokeHost() should return error when disabled")
	}
	if err := cc.PauseHost("any"); err == nil {
		t.Error("PauseHost() should return error when disabled")
	}
	if err := cc.UpdateRemoteContainer(context.Background(), "h", "c", "img", "dig"); err == nil {
		t.Error("UpdateRemoteContainer() should return error when disabled")
	}
	if err := cc.RollbackRemoteContainer(context.Background(), "h", "c"); err == nil {
		t.Error("RollbackRemoteContainer() should return error when disabled")
	}
}

func TestSetProviderEnablesAndDelegates(t *testing.T) {
	cc := NewClusterController()
	mock := &mockClusterProvider{
		hosts: []ClusterHost{
			{ID: "host-1", Name: "node-a", State: "active", EnrolledAt: time.Now()},
			{ID: "host-2", Name: "node-b", State: "draining", EnrolledAt: time.Now()},
		},
		connected: []string{"host-1"},
	}

	cc.SetProvider(mock)
	if !cc.Enabled() {
		t.Fatal("expected Enabled() = true after SetProvider")
	}

	// AllHosts delegates.
	hosts := cc.AllHosts()
	if len(hosts) != 2 {
		t.Fatalf("AllHosts() returned %d hosts, want 2", len(hosts))
	}
	if hosts[0].ID != "host-1" {
		t.Errorf("AllHosts()[0].ID = %q, want %q", hosts[0].ID, "host-1")
	}

	// GetHost delegates.
	h, found := cc.GetHost("host-2")
	if !found {
		t.Fatal("GetHost(host-2) returned false, want true")
	}
	if h.Name != "node-b" {
		t.Errorf("GetHost(host-2).Name = %q, want %q", h.Name, "node-b")
	}
	_, found = cc.GetHost("nonexistent")
	if found {
		t.Error("GetHost(nonexistent) should return false")
	}

	// ConnectedHosts delegates.
	conn := cc.ConnectedHosts()
	if len(conn) != 1 || conn[0] != "host-1" {
		t.Errorf("ConnectedHosts() = %v, want [host-1]", conn)
	}

	// GenerateEnrollToken delegates.
	tok, id, err := cc.GenerateEnrollToken()
	if err != nil {
		t.Fatalf("GenerateEnrollToken() error: %v", err)
	}
	if tok != "tok-abc" || id != "id-123" {
		t.Errorf("GenerateEnrollToken() = (%q, %q), want (tok-abc, id-123)", tok, id)
	}

	// Mutating methods delegate without error (mock returns nil).
	if err := cc.RemoveHost("host-1"); err != nil {
		t.Errorf("RemoveHost() error: %v", err)
	}
	if err := cc.RevokeHost("host-1"); err != nil {
		t.Errorf("RevokeHost() error: %v", err)
	}
	if err := cc.PauseHost("host-1"); err != nil {
		t.Errorf("PauseHost() error: %v", err)
	}
	if err := cc.UpdateRemoteContainer(context.Background(), "host-1", "nginx", "nginx:latest", "sha256:abc"); err != nil {
		t.Errorf("UpdateRemoteContainer() error: %v", err)
	}
	if err := cc.RollbackRemoteContainer(context.Background(), "host-1", "nginx"); err != nil {
		t.Errorf("RollbackRemoteContainer() error: %v", err)
	}
}

func TestSetProviderNilDisablesAgain(t *testing.T) {
	cc := NewClusterController()
	mock := &mockClusterProvider{
		hosts:     []ClusterHost{{ID: "h1", Name: "n1"}},
		connected: []string{"h1"},
	}

	cc.SetProvider(mock)
	if !cc.Enabled() {
		t.Fatal("expected enabled after SetProvider")
	}

	cc.SetProvider(nil)
	if cc.Enabled() {
		t.Error("expected disabled after SetProvider(nil)")
	}

	// Methods should return zero values again.
	if hosts := cc.AllHosts(); hosts != nil {
		t.Errorf("AllHosts() = %v after disable, want nil", hosts)
	}
	if _, _, err := cc.GenerateEnrollToken(); err == nil {
		t.Error("GenerateEnrollToken() should error after disable")
	}
}

func TestConcurrentAccess(t *testing.T) {
	cc := NewClusterController()
	mock := &mockClusterProvider{
		hosts:     []ClusterHost{{ID: "h1", Name: "n1"}},
		connected: []string{"h1"},
	}

	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	// Half the goroutines toggle the provider, the other half read from it.
	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			for range iterations {
				if id%2 == 0 {
					// Writer: toggle provider on/off.
					if id%4 == 0 {
						cc.SetProvider(mock)
					} else {
						cc.SetProvider(nil)
					}
				} else {
					// Reader: call all read methods.
					cc.Enabled()
					cc.AllHosts()
					cc.GetHost("h1")
					cc.ConnectedHosts()
					// Error return is expected when disabled; just ensure no panic.
					_, _, _ = cc.GenerateEnrollToken()
					_ = cc.RemoveHost("h1")
					_ = cc.UpdateRemoteContainer(context.Background(), "h1", "c", "i", "d")
					_ = cc.RollbackRemoteContainer(context.Background(), "h1", "c")
				}
			}
		}(g)
	}

	wg.Wait()
	// If we reach here without a race or panic, the test passes.
}

func TestDisabledMethodsReturnConsistentErrors(t *testing.T) {
	cc := NewClusterController()

	// All error-returning methods should include "cluster not enabled".
	errMethods := []struct {
		name string
		fn   func() error
	}{
		{"GenerateEnrollToken", func() error { _, _, err := cc.GenerateEnrollToken(); return err }},
		{"RemoveHost", func() error { return cc.RemoveHost("x") }},
		{"RevokeHost", func() error { return cc.RevokeHost("x") }},
		{"PauseHost", func() error { return cc.PauseHost("x") }},
		{"UpdateRemoteContainer", func() error {
			return cc.UpdateRemoteContainer(context.Background(), "h", "c", "i", "d")
		}},
		{"RollbackRemoteContainer", func() error {
			return cc.RollbackRemoteContainer(context.Background(), "h", "c")
		}},
	}

	for _, tc := range errMethods {
		err := tc.fn()
		if err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
			continue
		}
		want := "cluster not enabled"
		if got := fmt.Sprint(err); got != want {
			t.Errorf("%s: error = %q, want %q", tc.name, got, want)
		}
	}
}
