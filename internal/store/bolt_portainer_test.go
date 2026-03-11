package store

import (
	"testing"
)

func TestPortainerInstance_SaveAndGet(t *testing.T) {
	s := testStore(t)

	inst := PortainerInstance{
		ID:      "p1",
		Name:    "Main Portainer",
		URL:     "https://portainer.example.com:9443",
		Token:   "secret-token-abc",
		Enabled: true,
	}

	if err := s.SavePortainerInstance(inst); err != nil {
		t.Fatalf("SavePortainerInstance: %v", err)
	}

	got, err := s.GetPortainerInstance("p1")
	if err != nil {
		t.Fatalf("GetPortainerInstance: %v", err)
	}

	if got.ID != inst.ID {
		t.Errorf("ID = %q, want %q", got.ID, inst.ID)
	}
	if got.Name != inst.Name {
		t.Errorf("Name = %q, want %q", got.Name, inst.Name)
	}
	if got.URL != inst.URL {
		t.Errorf("URL = %q, want %q", got.URL, inst.URL)
	}
	if got.Token != inst.Token {
		t.Errorf("Token = %q, want %q", got.Token, inst.Token)
	}
	if got.Enabled != inst.Enabled {
		t.Errorf("Enabled = %v, want %v", got.Enabled, inst.Enabled)
	}
}

func TestPortainerInstance_ListMultiple(t *testing.T) {
	s := testStore(t)

	// Save 3 instances in non-alphabetical order to verify sorting.
	instances := []PortainerInstance{
		{ID: "p3", Name: "Third", URL: "https://p3.example.com", Token: "t3", Enabled: true},
		{ID: "p1", Name: "First", URL: "https://p1.example.com", Token: "t1", Enabled: true},
		{ID: "p2", Name: "Second", URL: "https://p2.example.com", Token: "t2", Enabled: false},
	}
	for _, inst := range instances {
		if err := s.SavePortainerInstance(inst); err != nil {
			t.Fatalf("SavePortainerInstance(%s): %v", inst.ID, err)
		}
	}

	got, err := s.ListPortainerInstances()
	if err != nil {
		t.Fatalf("ListPortainerInstances: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("got %d instances, want 3", len(got))
	}

	// Should be sorted by ID: p1, p2, p3.
	if got[0].ID != "p1" {
		t.Errorf("got[0].ID = %q, want p1", got[0].ID)
	}
	if got[1].ID != "p2" {
		t.Errorf("got[1].ID = %q, want p2", got[1].ID)
	}
	if got[2].ID != "p3" {
		t.Errorf("got[2].ID = %q, want p3", got[2].ID)
	}

	// Verify names came through correctly.
	if got[0].Name != "First" {
		t.Errorf("got[0].Name = %q, want First", got[0].Name)
	}
	if got[1].Enabled != false {
		t.Errorf("got[1].Enabled = %v, want false", got[1].Enabled)
	}
}

func TestPortainerInstance_ListEmpty(t *testing.T) {
	s := testStore(t)

	got, err := s.ListPortainerInstances()
	if err != nil {
		t.Fatalf("ListPortainerInstances: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d instances, want 0", len(got))
	}
}

func TestPortainerInstance_Delete(t *testing.T) {
	s := testStore(t)

	inst := PortainerInstance{
		ID:      "p1",
		Name:    "Doomed",
		URL:     "https://doomed.example.com",
		Token:   "tok",
		Enabled: true,
	}
	if err := s.SavePortainerInstance(inst); err != nil {
		t.Fatal(err)
	}

	// Verify it exists.
	if _, err := s.GetPortainerInstance("p1"); err != nil {
		t.Fatalf("expected instance to exist before delete: %v", err)
	}

	// Delete it.
	if err := s.DeletePortainerInstance("p1"); err != nil {
		t.Fatalf("DeletePortainerInstance: %v", err)
	}

	// Verify it's gone.
	_, err := s.GetPortainerInstance("p1")
	if err == nil {
		t.Error("expected error for deleted instance, got nil")
	}

	// Delete again — should be a silent no-op.
	if err := s.DeletePortainerInstance("p1"); err != nil {
		t.Fatalf("second DeletePortainerInstance should be no-op: %v", err)
	}
}

func TestPortainerInstance_NextID(t *testing.T) {
	s := testStore(t)

	// Empty store — should return p1.
	id, err := s.NextPortainerID()
	if err != nil {
		t.Fatalf("NextPortainerID (empty): %v", err)
	}
	if id != "p1" {
		t.Errorf("empty store: got %q, want p1", id)
	}

	// Add p1 and p3 (gap at p2). Next should be p4 (highest + 1).
	for _, inst := range []PortainerInstance{
		{ID: "p1", Name: "One", URL: "https://one.example.com", Token: "t1", Enabled: true},
		{ID: "p3", Name: "Three", URL: "https://three.example.com", Token: "t3", Enabled: true},
	} {
		if err := s.SavePortainerInstance(inst); err != nil {
			t.Fatal(err)
		}
	}

	id, err = s.NextPortainerID()
	if err != nil {
		t.Fatalf("NextPortainerID (p1+p3): %v", err)
	}
	if id != "p4" {
		t.Errorf("after p1+p3: got %q, want p4", id)
	}
}

func TestPortainerInstance_NextID_NonNumericKeys(t *testing.T) {
	s := testStore(t)

	// Insert a key that doesn't follow the pN pattern — should be ignored.
	inst := PortainerInstance{ID: "custom-id", Name: "Custom", URL: "https://x.com", Token: "t", Enabled: true}
	if err := s.SavePortainerInstance(inst); err != nil {
		t.Fatal(err)
	}

	id, err := s.NextPortainerID()
	if err != nil {
		t.Fatalf("NextPortainerID: %v", err)
	}
	// No valid pN keys, so maxNum stays 0 → p1.
	if id != "p1" {
		t.Errorf("got %q, want p1", id)
	}
}

func TestPortainerInstance_GetMissing(t *testing.T) {
	s := testStore(t)

	_, err := s.GetPortainerInstance("nonexistent")
	if err == nil {
		t.Error("expected error for missing instance, got nil")
	}
}

func TestPortainerInstance_Overwrite(t *testing.T) {
	s := testStore(t)

	inst := PortainerInstance{
		ID:      "p1",
		Name:    "Original",
		URL:     "https://original.example.com",
		Token:   "old-token",
		Enabled: true,
	}
	if err := s.SavePortainerInstance(inst); err != nil {
		t.Fatal(err)
	}

	// Overwrite with new values.
	inst.Name = "Updated"
	inst.URL = "https://updated.example.com"
	inst.Token = "new-token"
	inst.Enabled = false
	if err := s.SavePortainerInstance(inst); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetPortainerInstance("p1")
	if err != nil {
		t.Fatalf("GetPortainerInstance: %v", err)
	}
	if got.Name != "Updated" {
		t.Errorf("Name = %q, want Updated", got.Name)
	}
	if got.URL != "https://updated.example.com" {
		t.Errorf("URL = %q, want https://updated.example.com", got.URL)
	}
	if got.Token != "new-token" {
		t.Errorf("Token = %q, want new-token", got.Token)
	}
	if got.Enabled != false {
		t.Errorf("Enabled = %v, want false", got.Enabled)
	}
}

func TestPortainerInstance_EndpointConfig(t *testing.T) {
	s := testStore(t)

	inst := PortainerInstance{
		ID:      "p1",
		Name:    "With Endpoints",
		URL:     "https://portainer.example.com",
		Token:   "tok",
		Enabled: true,
		Endpoints: map[string]EndpointConfig{
			"1": {Enabled: true, Blocked: false},
			"2": {Enabled: false, Blocked: true, Reason: "unreachable"},
			"5": {Enabled: true, Blocked: false},
		},
	}

	if err := s.SavePortainerInstance(inst); err != nil {
		t.Fatalf("SavePortainerInstance: %v", err)
	}

	got, err := s.GetPortainerInstance("p1")
	if err != nil {
		t.Fatalf("GetPortainerInstance: %v", err)
	}

	if len(got.Endpoints) != 3 {
		t.Fatalf("got %d endpoints, want 3", len(got.Endpoints))
	}

	// Check enabled endpoint.
	ep1, ok := got.Endpoints["1"]
	if !ok {
		t.Fatal("endpoint 1 not found")
	}
	if !ep1.Enabled {
		t.Error("endpoint 1: Enabled = false, want true")
	}
	if ep1.Blocked {
		t.Error("endpoint 1: Blocked = true, want false")
	}

	// Check blocked endpoint.
	ep2, ok := got.Endpoints["2"]
	if !ok {
		t.Fatal("endpoint 2 not found")
	}
	if ep2.Enabled {
		t.Error("endpoint 2: Enabled = true, want false")
	}
	if !ep2.Blocked {
		t.Error("endpoint 2: Blocked = false, want true")
	}
	if ep2.Reason != "unreachable" {
		t.Errorf("endpoint 2: Reason = %q, want unreachable", ep2.Reason)
	}
}

func TestPortainerInstance_NilEndpoints(t *testing.T) {
	s := testStore(t)

	// Instance with no endpoints — Endpoints should be nil/empty after round-trip.
	inst := PortainerInstance{
		ID:      "p1",
		Name:    "No Endpoints",
		URL:     "https://bare.example.com",
		Token:   "tok",
		Enabled: true,
	}
	if err := s.SavePortainerInstance(inst); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetPortainerInstance("p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Endpoints) != 0 {
		t.Errorf("expected nil/empty endpoints, got %v", got.Endpoints)
	}
}

func TestPortainerMigration_OldSettingsToInstance(t *testing.T) {
	s := testStore(t)

	// Simulate old-style settings.
	_ = s.SaveSetting(SettingPortainerURL, "https://portainer.example.com")
	_ = s.SaveSetting(SettingPortainerToken, "ptr_old_token")
	_ = s.SaveSetting(SettingPortainerEnabled, "true")

	migrated, err := s.MigratePortainerSettings()
	if err != nil {
		t.Fatalf("MigratePortainerSettings: %v", err)
	}
	if !migrated {
		t.Fatal("expected migration to happen")
	}

	// Instance should exist.
	inst, err := s.GetPortainerInstance("p1")
	if err != nil {
		t.Fatalf("GetPortainerInstance: %v", err)
	}
	if inst.URL != "https://portainer.example.com" {
		t.Errorf("URL = %q", inst.URL)
	}
	if inst.Token != "ptr_old_token" {
		t.Errorf("Token = %q", inst.Token)
	}
	if !inst.Enabled {
		t.Error("expected enabled=true")
	}
	if inst.Name != "Portainer" {
		t.Errorf("Name = %q, want Portainer", inst.Name)
	}

	// Old settings should be cleared.
	if v, _ := s.LoadSetting(SettingPortainerURL); v != "" {
		t.Errorf("old URL not cleared: %q", v)
	}
	if v, _ := s.LoadSetting(SettingPortainerToken); v != "" {
		t.Errorf("old token not cleared: %q", v)
	}
	if v, _ := s.LoadSetting(SettingPortainerEnabled); v != "" {
		t.Errorf("old enabled not cleared: %q", v)
	}
}

func TestPortainerMigration_NoOldSettings(t *testing.T) {
	s := testStore(t)

	migrated, err := s.MigratePortainerSettings()
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Error("should not migrate when no old settings exist")
	}
}

func TestPortainerMigration_AlreadyMigrated(t *testing.T) {
	s := testStore(t)

	// Old settings exist but instance already exists (re-run safety).
	_ = s.SaveSetting(SettingPortainerURL, "https://portainer.example.com")
	_ = s.SaveSetting(SettingPortainerToken, "ptr_token")
	_ = s.SavePortainerInstance(PortainerInstance{ID: "p1", Name: "Already"})

	migrated, err := s.MigratePortainerSettings()
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Error("should skip migration when instances already exist")
	}
}
