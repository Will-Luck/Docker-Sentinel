package store

import "testing"

// ---------------------------------------------------------------------------
// Cluster Hosts
// ---------------------------------------------------------------------------

func TestClusterHostRoundTrip(t *testing.T) {
	s := testStore(t)

	got, err := s.GetClusterHost("host-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for missing host, got %q", got)
	}

	data := []byte(`{"name":"node-1","ip":"10.0.0.1"}`)
	if err := s.SaveClusterHost("host-1", data); err != nil {
		t.Fatal(err)
	}

	got, err = s.GetClusterHost("host-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

func TestListClusterHosts(t *testing.T) {
	s := testStore(t)

	if err := s.SaveClusterHost("h1", []byte("data-1")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveClusterHost("h2", []byte("data-2")); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListClusterHosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}
	if string(all["h1"]) != "data-1" {
		t.Errorf("h1 = %q, want %q", all["h1"], "data-1")
	}
	if string(all["h2"]) != "data-2" {
		t.Errorf("h2 = %q, want %q", all["h2"], "data-2")
	}
}

func TestListClusterHostsEmpty(t *testing.T) {
	s := testStore(t)

	all, err := s.ListClusterHosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("expected empty map, got %v", all)
	}
}

func TestDeleteClusterHost(t *testing.T) {
	s := testStore(t)

	if err := s.SaveClusterHost("h1", []byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteClusterHost("h1"); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetClusterHost("h1")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Enrollment Tokens
// ---------------------------------------------------------------------------

func TestEnrollTokenRoundTrip(t *testing.T) {
	s := testStore(t)

	got, err := s.GetEnrollToken("tok-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for missing token, got %q", got)
	}

	data := []byte(`{"hash":"abc123","expires":"2025-12-31"}`)
	if err := s.SaveEnrollToken("tok-1", data); err != nil {
		t.Fatal(err)
	}

	got, err = s.GetEnrollToken("tok-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

func TestDeleteEnrollToken(t *testing.T) {
	s := testStore(t)

	if err := s.SaveEnrollToken("tok-1", []byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEnrollToken("tok-1"); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetEnrollToken("tok-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Certificate Revocation
// ---------------------------------------------------------------------------

func TestRevokedCertRoundTrip(t *testing.T) {
	s := testStore(t)

	revoked, err := s.IsRevokedCert("SERIAL-001")
	if err != nil {
		t.Fatal(err)
	}
	if revoked {
		t.Error("expected false for unknown serial")
	}

	if err := s.AddRevokedCert("SERIAL-001"); err != nil {
		t.Fatal(err)
	}

	revoked, err = s.IsRevokedCert("SERIAL-001")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Error("expected true after adding")
	}

	// Other serial should not be revoked.
	revoked, err = s.IsRevokedCert("SERIAL-999")
	if err != nil {
		t.Fatal(err)
	}
	if revoked {
		t.Error("expected false for different serial")
	}
}

func TestListRevokedCerts(t *testing.T) {
	s := testStore(t)

	if err := s.AddRevokedCert("SN-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRevokedCert("SN-2"); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListRevokedCerts()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
	// Each value should be an RFC3339 timestamp.
	for serial, ts := range all {
		if ts == "" {
			t.Errorf("empty timestamp for serial %s", serial)
		}
	}
}

func TestListRevokedCertsEmpty(t *testing.T) {
	s := testStore(t)

	all, err := s.ListRevokedCerts()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("expected empty map, got %v", all)
	}
}

// ---------------------------------------------------------------------------
// Cluster Journal
// ---------------------------------------------------------------------------

func TestClusterJournalRoundTrip(t *testing.T) {
	s := testStore(t)

	if err := s.SaveClusterJournal("j1", []byte("action-1")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveClusterJournal("j2", []byte("action-2")); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListClusterJournal()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}
	if string(all["j1"]) != "action-1" {
		t.Errorf("j1 = %q, want %q", all["j1"], "action-1")
	}
}

func TestClearClusterJournal(t *testing.T) {
	s := testStore(t)

	if err := s.SaveClusterJournal("j1", []byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveClusterJournal("j2", []byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearClusterJournal(); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListClusterJournal()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("expected empty after clear, got %d", len(all))
	}
}

func TestClearClusterJournalEmpty(t *testing.T) {
	s := testStore(t)

	// Clearing an already-empty journal should not error.
	if err := s.ClearClusterJournal(); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Cluster Config Cache
// ---------------------------------------------------------------------------

func TestClusterConfigCacheRoundTrip(t *testing.T) {
	s := testStore(t)

	got, err := s.GetClusterConfigCache("policies")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for missing key, got %q", got)
	}

	data := []byte(`{"default":"auto"}`)
	if err := s.SaveClusterConfigCache("policies", data); err != nil {
		t.Fatal(err)
	}

	got, err = s.GetClusterConfigCache("policies")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

func TestClusterConfigCacheOverwrite(t *testing.T) {
	s := testStore(t)

	if err := s.SaveClusterConfigCache("key", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveClusterConfigCache("key", []byte("v2")); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetClusterConfigCache("key")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2" {
		t.Errorf("expected %q, got %q", "v2", got)
	}
}

// ---------------------------------------------------------------------------
// ScopedKey helper
// ---------------------------------------------------------------------------

func TestScopedKey(t *testing.T) {
	tests := []struct {
		hostID string
		name   string
		want   string
	}{
		{"", "nginx", "nginx"},
		{"host-1", "nginx", "host-1::nginx"},
		{"", "my-app", "my-app"},
		{"remote-node", "redis", "remote-node::redis"},
	}
	for _, tt := range tests {
		got := ScopedKey(tt.hostID, tt.name)
		if got != tt.want {
			t.Errorf("ScopedKey(%q, %q) = %q, want %q", tt.hostID, tt.name, got, tt.want)
		}
	}
}
