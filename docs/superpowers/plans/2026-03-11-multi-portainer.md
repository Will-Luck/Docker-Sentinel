# Multi-Instance Portainer Integration — Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Support multiple Portainer instances with per-endpoint toggles, local socket detection, and dashboard integration as host groups.

**Architecture:** New `portainer_instances` BoltDB bucket stores instance configs as JSON. The engine iterates a list of `PortainerInstance` structs instead of a single scanner. The web layer exposes CRUD API endpoints; the connectors page renders instance cards. Dashboard shows Portainer endpoints as host groups using the same pattern as cluster hosts. Migration converts old flat settings to a single instance record on first boot.

**Tech Stack:** Go 1.24, BoltDB, htmx, vanilla JS (esbuild-bundled modules)

**Spec:** `docs/superpowers/specs/2026-03-11-multi-portainer-design.md`

---

## Chunk 1: Store Layer — Data Model, CRUD, Migration

### Task 1: Portainer Instance Types and Bucket

**Files:**
- Create: `internal/store/bolt_portainer.go`
- Modify: `internal/store/bolt.go:15-41` (add bucket constant)
- Modify: `internal/store/bolt.go:148` (register bucket in Open)
- Test: `internal/store/bolt_portainer_test.go`

- [ ] **Step 1: Write the failing test for instance round-trip**

```go
// bolt_portainer_test.go
package store

import "testing"

func TestPortainerInstance_SaveAndGet(t *testing.T) {
	s := testStore(t)

	inst := PortainerInstance{
		ID:      "p1",
		Name:    "Office",
		URL:     "https://portainer.office.com",
		Token:   "ptr_abc123",
		Enabled: true,
	}
	if err := s.SavePortainerInstance(inst); err != nil {
		t.Fatalf("SavePortainerInstance: %v", err)
	}

	got, err := s.GetPortainerInstance("p1")
	if err != nil {
		t.Fatalf("GetPortainerInstance: %v", err)
	}
	if got.Name != "Office" || got.URL != "https://portainer.office.com" {
		t.Errorf("got %+v, want name=Office url=https://portainer.office.com", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/store/ -run TestPortainerInstance_SaveAndGet -v`
Expected: FAIL — `PortainerInstance` type undefined

- [ ] **Step 3: Implement types, bucket, and CRUD**

In `internal/store/bolt.go`, add the bucket constant (after line 40, before the closing paren):

```go
	bucketPortainerInstances = []byte("portainer_instances")
```

In the `Open` function (line 148), add `bucketPortainerInstances` to the bucket slice.

Create `internal/store/bolt_portainer.go`:

```go
package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// PortainerInstance represents a configured Portainer server.
type PortainerInstance struct {
	ID        string                       `json:"id"`
	Name      string                       `json:"name"`
	URL       string                       `json:"url"`
	Token     string                       `json:"token"`
	Enabled   bool                         `json:"enabled"`
	Endpoints map[string]EndpointConfig    `json:"endpoints,omitempty"`
}

// EndpointConfig stores per-endpoint user/auto settings.
type EndpointConfig struct {
	Enabled bool   `json:"enabled"`
	Blocked bool   `json:"blocked,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// SavePortainerInstance upserts a Portainer instance record.
func (s *Store) SavePortainerInstance(inst PortainerInstance) error {
	data, err := json.Marshal(inst)
	if err != nil {
		return fmt.Errorf("marshal portainer instance: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := bucket(tx, bucketPortainerInstances)
		if err != nil {
			return err
		}
		return b.Put([]byte(inst.ID), data)
	})
}

// GetPortainerInstance loads a single instance by ID.
func (s *Store) GetPortainerInstance(id string) (PortainerInstance, error) {
	var inst PortainerInstance
	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := bucket(tx, bucketPortainerInstances)
		if err != nil {
			return err
		}
		v := b.Get([]byte(id))
		if v == nil {
			return fmt.Errorf("portainer instance %q not found", id)
		}
		return json.Unmarshal(v, &inst)
	})
	return inst, err
}

// ListPortainerInstances returns all configured instances, sorted by ID.
func (s *Store) ListPortainerInstances() ([]PortainerInstance, error) {
	var instances []PortainerInstance
	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := bucket(tx, bucketPortainerInstances)
		if err != nil {
			return err
		}
		return b.ForEach(func(k, v []byte) error {
			var inst PortainerInstance
			if err := json.Unmarshal(v, &inst); err != nil {
				return err
			}
			instances = append(instances, inst)
			return nil
		})
	})
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].ID < instances[j].ID
	})
	return instances, err
}

// DeletePortainerInstance removes an instance by ID.
func (s *Store) DeletePortainerInstance(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := bucket(tx, bucketPortainerInstances)
		if err != nil {
			return err
		}
		return b.Delete([]byte(id))
	})
}

// NextPortainerID returns the next available instance ID (p1, p2, ...).
func (s *Store) NextPortainerID() (string, error) {
	var maxNum int
	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := bucket(tx, bucketPortainerInstances)
		if err != nil {
			return err
		}
		return b.ForEach(func(k, _ []byte) error {
			key := string(k)
			if strings.HasPrefix(key, "p") {
				if n, err := strconv.Atoi(key[1:]); err == nil && n > maxNum {
					maxNum = n
				}
			}
			return nil
		})
	})
	return fmt.Sprintf("p%d", maxNum+1), err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/store/ -run TestPortainerInstance_SaveAndGet -v`
Expected: PASS

- [ ] **Step 5: Write tests for List, Delete, NextID**

Add to `bolt_portainer_test.go`:

```go
func TestPortainerInstance_ListMultiple(t *testing.T) {
	s := testStore(t)

	for _, id := range []string{"p2", "p1", "p3"} {
		_ = s.SavePortainerInstance(PortainerInstance{ID: id, Name: "inst-" + id})
	}
	list, err := s.ListPortainerInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d instances, want 3", len(list))
	}
	// Verify sorted by ID.
	if list[0].ID != "p1" || list[1].ID != "p2" || list[2].ID != "p3" {
		t.Errorf("not sorted: %v %v %v", list[0].ID, list[1].ID, list[2].ID)
	}
}

func TestPortainerInstance_Delete(t *testing.T) {
	s := testStore(t)

	_ = s.SavePortainerInstance(PortainerInstance{ID: "p1", Name: "test"})
	if err := s.DeletePortainerInstance("p1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := s.GetPortainerInstance("p1")
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}

func TestPortainerInstance_NextID(t *testing.T) {
	s := testStore(t)

	// Empty store -> p1.
	id, _ := s.NextPortainerID()
	if id != "p1" {
		t.Errorf("got %q, want p1", id)
	}

	// After p1, p3 -> p4 (gaps don't matter, takes max+1).
	_ = s.SavePortainerInstance(PortainerInstance{ID: "p1"})
	_ = s.SavePortainerInstance(PortainerInstance{ID: "p3"})
	id, _ = s.NextPortainerID()
	if id != "p4" {
		t.Errorf("got %q, want p4", id)
	}
}

func TestPortainerInstance_GetMissing(t *testing.T) {
	s := testStore(t)

	_, err := s.GetPortainerInstance("p99")
	if err == nil {
		t.Fatal("expected error for missing instance")
	}
}

func TestPortainerInstance_Overwrite(t *testing.T) {
	s := testStore(t)

	_ = s.SavePortainerInstance(PortainerInstance{ID: "p1", Name: "old"})
	_ = s.SavePortainerInstance(PortainerInstance{ID: "p1", Name: "new"})

	got, _ := s.GetPortainerInstance("p1")
	if got.Name != "new" {
		t.Errorf("got name %q, want new", got.Name)
	}
}

func TestPortainerInstance_EndpointConfig(t *testing.T) {
	s := testStore(t)

	inst := PortainerInstance{
		ID:   "p1",
		Name: "test",
		Endpoints: map[string]EndpointConfig{
			"3": {Enabled: true},
			"7": {Blocked: true, Reason: "local_socket"},
		},
	}
	_ = s.SavePortainerInstance(inst)

	got, _ := s.GetPortainerInstance("p1")
	if !got.Endpoints["3"].Enabled {
		t.Error("endpoint 3 should be enabled")
	}
	if !got.Endpoints["7"].Blocked || got.Endpoints["7"].Reason != "local_socket" {
		t.Error("endpoint 7 should be blocked with local_socket reason")
	}
}
```

- [ ] **Step 6: Run all store tests**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/store/ -v -count=1`
Expected: All PASS

- [ ] **Step 7: Commit**

```bash
cd /home/lns/Docker-Sentinel
git add internal/store/bolt.go internal/store/bolt_portainer.go internal/store/bolt_portainer_test.go
git commit -m "feat: add portainer_instances BoltDB bucket with CRUD operations"
```

### Task 2: Migration — Old Settings to Instance Record

**Files:**
- Modify: `internal/store/bolt_portainer.go` (add MigratePortainerSettings)
- Test: `internal/store/bolt_portainer_test.go`

- [ ] **Step 1: Write the failing migration test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/store/ -run TestPortainerMigration -v`
Expected: FAIL — `MigratePortainerSettings` undefined

- [ ] **Step 3: Implement migration**

Add to `internal/store/bolt_portainer.go`:

```go
// MigratePortainerSettings converts old flat portainer_url/portainer_token
// settings into a PortainerInstance record. Returns true if migration occurred.
// Safe to call multiple times: skips if instances already exist.
func (s *Store) MigratePortainerSettings() (bool, error) {
	// Check if already migrated (instances exist).
	existing, err := s.ListPortainerInstances()
	if err != nil {
		return false, fmt.Errorf("list instances: %w", err)
	}
	if len(existing) > 0 {
		return false, nil
	}

	// Check for old settings.
	url, _ := s.LoadSetting(SettingPortainerURL)
	token, _ := s.LoadSetting(SettingPortainerToken)
	if url == "" && token == "" {
		return false, nil
	}

	enabled, _ := s.LoadSetting(SettingPortainerEnabled)

	inst := PortainerInstance{
		ID:      "p1",
		Name:    "Portainer",
		URL:     url,
		Token:   token,
		Enabled: enabled == "true",
	}
	if err := s.SavePortainerInstance(inst); err != nil {
		return false, fmt.Errorf("save migrated instance: %w", err)
	}

	// Clear old settings.
	_ = s.DeleteSetting(SettingPortainerURL)
	_ = s.DeleteSetting(SettingPortainerToken)
	_ = s.DeleteSetting(SettingPortainerEnabled)

	return true, nil
}
```

Also add `DeleteSetting` to `bolt.go` (it does not exist yet):

```go
// DeleteSetting removes a setting key.
func (s *Store) DeleteSetting(key string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := bucket(tx, bucketSettings)
		if err != nil {
			return err
		}
		return b.Delete([]byte(key))
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/store/ -run TestPortainerMigration -v`
Expected: All PASS

- [ ] **Step 5: Commit**

```bash
cd /home/lns/Docker-Sentinel
git add internal/store/bolt_portainer.go internal/store/bolt_portainer_test.go
git commit -m "feat: add migration from old portainer settings to instance record"
```

### Task 3: Queue/History HostID Migration

The queue stores a single JSON array under `bucketQueue` key `"pending"`. Each `PendingUpdate` has a `HostID` field (e.g. `"portainer:3"`) and `ContainerName`. The queue map key is `HostID + "::" + ContainerName`. Migration must rewrite the `HostID` field from `"portainer:N"` to `"portainer:p1:N"`.

History records are stored with RFC3339Nano timestamps as BoltDB keys. Each `UpdateRecord` JSON value contains `HostID` and `ContainerName` fields. Migration iterates values and rewrites the `HostID` field.

**Files:**
- Modify: `internal/store/bolt_portainer.go` (add MigratePortainerKeys)
- Modify: `internal/store/bolt_portainer_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestPortainerMigration_QueueHostIDRewrite(t *testing.T) {
	s := testStore(t)

	// Seed old-format queue entry. The queue is a JSON array stored under
	// the single key "pending" in bucketQueue. PendingUpdate.HostID holds
	// the host identifier; Key() returns "hostID::containerName".
	oldJSON := `[{"container_name":"nginx","host_id":"portainer:3","current_image":"nginx:1.25"}]`
	if err := s.SavePendingQueue([]byte(oldJSON)); err != nil {
		t.Fatal(err)
	}

	if err := s.MigratePortainerKeys("p1"); err != nil {
		t.Fatalf("MigratePortainerKeys: %v", err)
	}

	// Reload and check HostID is rewritten.
	data, err := s.LoadPendingQueue()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"portainer:p1:3"`) {
		t.Errorf("expected portainer:p1:3 in queue, got: %s", data)
	}
	if strings.Contains(string(data), `"host_id":"portainer:3"`) {
		t.Error("old host_id portainer:3 still present")
	}
}

func TestPortainerMigration_HistoryHostIDRewrite(t *testing.T) {
	s := testStore(t)

	// Seed old-format history record.
	rec := UpdateRecord{
		Timestamp:     time.Now(),
		ContainerName: "nginx",
		HostID:        "portainer:5",
		OldImage:      "nginx:1.24",
		NewImage:      "nginx:1.25",
		Outcome:       "success",
	}
	if err := s.RecordUpdate(rec); err != nil {
		t.Fatal(err)
	}

	if err := s.MigratePortainerKeys("p1"); err != nil {
		t.Fatal(err)
	}

	// Verify HostID rewritten.
	records, err := s.ListHistory(10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if records[0].HostID != "portainer:p1:5" {
		t.Errorf("HostID = %q, want portainer:p1:5", records[0].HostID)
	}
}

func TestPortainerMigration_SkipsAlreadyMigrated(t *testing.T) {
	s := testStore(t)

	// Queue entry already in new format.
	newJSON := `[{"container_name":"nginx","host_id":"portainer:p1:3","current_image":"nginx:1.25"}]`
	_ = s.SavePendingQueue([]byte(newJSON))

	if err := s.MigratePortainerKeys("p1"); err != nil {
		t.Fatal(err)
	}

	data, _ := s.LoadPendingQueue()
	// Should be unchanged (still portainer:p1:3, not portainer:p1:p1:3).
	if strings.Contains(string(data), "portainer:p1:p1") {
		t.Error("double-migrated: portainer:p1:p1 found")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/store/ -run TestPortainerMigration_Queue -v`
Expected: FAIL — `MigratePortainerKeys` undefined

- [ ] **Step 3: Implement key migration**

Add to `internal/store/bolt_portainer.go`:

```go
// MigratePortainerKeys rewrites HostID fields in queue and history entries
// from the old format "portainer:{epID}" to "portainer:{instanceID}:{epID}".
// Queue: single JSON array under key "pending" — unmarshal, rewrite HostIDs, re-save.
// History: each value is a JSON UpdateRecord — rewrite HostID in each.
func (s *Store) MigratePortainerKeys(instanceID string) error {
	prefix := "portainer:"
	newPrefix := "portainer:" + instanceID + ":"

	// --- Queue migration ---
	data, err := s.LoadPendingQueue()
	if err != nil {
		return fmt.Errorf("load queue: %w", err)
	}
	if data != nil {
		var items []json.RawMessage
		if json.Unmarshal(data, &items) == nil {
			changed := false
			for i, raw := range items {
				var m map[string]interface{}
				if json.Unmarshal(raw, &m) != nil {
					continue
				}
				hostID, _ := m["host_id"].(string)
				if !strings.HasPrefix(hostID, prefix) {
					continue
				}
				rest := hostID[len(prefix):]
				// Skip already-migrated entries (rest starts with instance ID prefix like "p1:").
				if len(rest) > 0 && rest[0] == 'p' {
					continue
				}
				m["host_id"] = newPrefix + rest
				rewritten, err := json.Marshal(m)
				if err != nil {
					continue
				}
				items[i] = rewritten
				changed = true
			}
			if changed {
				newData, err := json.Marshal(items)
				if err != nil {
					return fmt.Errorf("marshal queue: %w", err)
				}
				if err := s.SavePendingQueue(newData); err != nil {
					return fmt.Errorf("save queue: %w", err)
				}
			}
		}
	}

	// --- History migration ---
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := bucket(tx, bucketHistory)
		if err != nil {
			return err
		}
		type kv struct{ key, val []byte }
		var rewrites []kv
		if err := b.ForEach(func(k, v []byte) error {
			var rec UpdateRecord
			if json.Unmarshal(v, &rec) != nil {
				return nil
			}
			if !strings.HasPrefix(rec.HostID, prefix) {
				return nil
			}
			rest := rec.HostID[len(prefix):]
			if len(rest) > 0 && rest[0] == 'p' {
				return nil // already migrated
			}
			rec.HostID = newPrefix + rest
			newVal, err := json.Marshal(rec)
			if err != nil {
				return nil
			}
			keyCopy := make([]byte, len(k))
			copy(keyCopy, k)
			rewrites = append(rewrites, kv{key: keyCopy, val: newVal})
			return nil
		}); err != nil {
			return err
		}
		for _, rw := range rewrites {
			if err := b.Put(rw.key, rw.val); err != nil {
				return err
			}
		}
		return nil
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/store/ -run TestPortainerMigration -v`
Expected: All PASS

- [ ] **Step 5: Run all store tests**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/store/ -v -count=1`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
cd /home/lns/Docker-Sentinel
git add internal/store/bolt_portainer.go internal/store/bolt_portainer_test.go
git commit -m "feat: migrate queue/history HostID fields for portainer instance IDs"
```

---

## Chunk 2: Engine Layer — Multi-Instance Scanning

### Task 4: PortainerInstance Type and Updater Field

**Files:**
- Modify: `internal/engine/updater_scan.go:62-87` (add PortainerInstance, EndpointConfig)
- Modify: `internal/engine/updater.go:75` (replace `portainer` with `portainerInstances`)
- Modify: `internal/engine/updater.go:144-148` (update setter)
- Modify: `internal/engine/updater_remote.go:257-279` (iterate instances)

- [ ] **Step 1: Add PortainerInstance and EndpointConfig to engine types**

In `internal/engine/updater_scan.go`, after `PortainerContainerResult` (after line 87), add:

```go
// EndpointConfig holds per-endpoint user and auto-detected settings.
type EndpointConfig struct {
	Enabled bool
	Blocked bool
}

// PortainerInstance represents a single Portainer server with its scanner
// and per-endpoint configuration. The engine iterates all instances during scan.
type PortainerInstance struct {
	ID        string
	Name      string
	Scanner   PortainerScanner
	Endpoints map[int]EndpointConfig
}
```

- [ ] **Step 2: Replace single portainer field with instance list**

In `internal/engine/updater.go`, change line 75:

```go
// Old:
portainer        PortainerScanner
// New:
portainerInstances []PortainerInstance
```

Replace `SetPortainerScanner` (lines 144-148) with:

```go
// SetPortainerInstances replaces the full list of Portainer instances.
func (u *Updater) SetPortainerInstances(instances []PortainerInstance) {
	u.portainerInstances = instances
}

// AddPortainerInstance appends or replaces a single instance by ID.
func (u *Updater) AddPortainerInstance(inst PortainerInstance) {
	for i, existing := range u.portainerInstances {
		if existing.ID == inst.ID {
			u.portainerInstances[i] = inst
			return
		}
	}
	u.portainerInstances = append(u.portainerInstances, inst)
}

// RemovePortainerInstance removes an instance by ID.
func (u *Updater) RemovePortainerInstance(id string) {
	for i, inst := range u.portainerInstances {
		if inst.ID == id {
			u.portainerInstances = append(u.portainerInstances[:i], u.portainerInstances[i+1:]...)
			return
		}
	}
}
```

- [ ] **Step 3: Update scan entry point**

In `internal/engine/updater_scan.go`, find the call to `scanPortainerEndpoints` (around line 758). Change:

```go
// Old:
if u.portainer != nil {
    localIDs := make(map[string]bool, len(containers))
    for _, c := range containers {
        localIDs[c.ID] = true
    }
    u.scanPortainerEndpoints(ctx, mode, &result, filters, reserve, localIDs)
}

// New:
if len(u.portainerInstances) > 0 {
    localIDs := make(map[string]bool, len(containers))
    for _, c := range containers {
        localIDs[c.ID] = true
    }
    u.scanPortainerInstances(ctx, mode, &result, filters, reserve, localIDs)
}
```

- [ ] **Step 4: Rewrite scanPortainerEndpoints to iterate instances**

In `internal/engine/updater_remote.go`, replace `scanPortainerEndpoints` (lines 257-279) with:

```go
// scanPortainerInstances iterates all configured Portainer instances and their endpoints.
func (u *Updater) scanPortainerInstances(ctx context.Context, mode ScanMode, result *ScanResult, filters []string, reserve int, localIDs map[string]bool) {
	for i := range u.portainerInstances {
		if ctx.Err() != nil {
			return
		}
		inst := &u.portainerInstances[i]
		inst.Scanner.ResetCache()

		endpoints, err := inst.Scanner.Endpoints(ctx)
		if err != nil {
			u.log.Warn("failed to list Portainer endpoints", "instance", inst.Name, "error", err)
			continue
		}

		u.log.Info("scanning Portainer instance", "instance", inst.Name, "endpoints", len(endpoints))

		for _, ep := range endpoints {
			if ctx.Err() != nil {
				return
			}
			// Skip blocked or disabled endpoints.
			if cfg, ok := inst.Endpoints[ep.ID]; ok {
				if cfg.Blocked || !cfg.Enabled {
					u.log.Debug("skipping disabled/blocked Portainer endpoint",
						"instance", inst.Name, "endpoint", ep.Name)
					continue
				}
			}
			u.scanPortainerEndpoint(ctx, inst, ep, mode, result, filters, reserve, localIDs)
		}
	}
}
```

- [ ] **Step 5: Update scanPortainerEndpoint signature and key format**

In `internal/engine/updater_remote.go`, update `scanPortainerEndpoint` (line 281):

```go
// Old signature:
func (u *Updater) scanPortainerEndpoint(ctx context.Context, ep PortainerEndpointInfo, ...)

// New signature:
func (u *Updater) scanPortainerEndpoint(ctx context.Context, inst *PortainerInstance, ep PortainerEndpointInfo, ...)
```

Update the `hostID` line (line 291):

```go
// Old:
hostID := fmt.Sprintf("portainer:%d", ep.ID)
// New:
hostID := fmt.Sprintf("portainer:%s:%d", inst.ID, ep.ID)
```

Update the log message at line 289 to include instance name:

```go
u.log.Info("scanning Portainer endpoint", "instance", inst.Name, "endpoint", ep.Name, "containers", len(containers))
```

- [ ] **Step 6: Fix all remaining references to u.portainer**

Search for `u.portainer` in the engine package and update every occurrence:

**a) Prune section (`updater_scan.go:384-399`)** — builds `liveNames` for queue pruning. Currently uses `u.portainer.Endpoints()` and `u.portainer.EndpointContainers()` directly. Rewrite to iterate `u.portainerInstances`:

```go
// Old (lines 384-399):
if u.portainer != nil {
    endpoints, epErr := u.portainer.Endpoints(ctx)
    if epErr == nil {
        for _, ep := range endpoints {
            epContainers, ecErr := u.portainer.EndpointContainers(ctx, ep.ID)
            // ...
            hostID := fmt.Sprintf("portainer:%d", ep.ID)
            // ...
        }
    }
}

// New:
for i := range u.portainerInstances {
    inst := &u.portainerInstances[i]
    endpoints, epErr := inst.Scanner.Endpoints(ctx)
    if epErr != nil {
        continue
    }
    for _, ep := range endpoints {
        if cfg, ok := inst.Endpoints[ep.ID]; ok && (cfg.Blocked || !cfg.Enabled) {
            continue
        }
        epContainers, ecErr := inst.Scanner.EndpointContainers(ctx, ep.ID)
        if ecErr != nil {
            continue
        }
        hostID := fmt.Sprintf("portainer:%s:%d", inst.ID, ep.ID)
        for _, pc := range epContainers {
            liveNames[store.ScopedKey(hostID, pc.Name)] = true
        }
    }
}
```

**b) scanPortainerEndpoint body** — calls to `u.portainer.RedeployStack` and `u.portainer.UpdateStandaloneContainer` (around lines 455-467) must use `inst.Scanner.RedeployStack` and `inst.Scanner.UpdateStandaloneContainer` since the function now receives `inst *PortainerInstance`

**c) Any other nil checks** — `u.portainer != nil` becomes `len(u.portainerInstances) > 0`

- [ ] **Step 7: Run tests**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/engine/ -v -count=1`
Expected: All PASS (update test mocks if needed)

- [ ] **Step 8: Run full test suite**

Run: `cd /home/lns/Docker-Sentinel && go test ./... -count=1`
Expected: All PASS

- [ ] **Step 9: Commit**

```bash
cd /home/lns/Docker-Sentinel
git add internal/engine/
git commit -m "feat: multi-instance Portainer scanning with per-endpoint filtering"
```

---

## Chunk 3: Local Socket Detection

### Task 5: IsLocalSocket Helper in Portainer Package

**Files:**
- Modify: `internal/portainer/types.go` (add IsLocalSocket method)
- Test: `internal/portainer/types_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/portainer/types_test.go`:

```go
package portainer

import "testing"

func TestEndpoint_IsLocalSocket(t *testing.T) {
	tests := []struct {
		name   string
		ep     Endpoint
		want   bool
	}{
		{
			name: "unix socket URL",
			ep:   Endpoint{URL: "unix:///var/run/docker.sock", Type: EndpointDocker},
			want: true,
		},
		{
			name: "empty URL with Docker type",
			ep:   Endpoint{URL: "", Type: EndpointDocker},
			want: true,
		},
		{
			name: "TCP URL with Docker type",
			ep:   Endpoint{URL: "tcp://192.168.1.100:2375", Type: EndpointDocker},
			want: false,
		},
		{
			name: "agent endpoint",
			ep:   Endpoint{URL: "tcp://192.168.1.100:9001", Type: EndpointAgentDocker},
			want: false,
		},
		{
			name: "empty URL with agent type",
			ep:   Endpoint{URL: "", Type: EndpointAgentDocker},
			want: false,
		},
		{
			name: "kubernetes endpoint",
			ep:   Endpoint{URL: "", Type: EndpointKubernetes},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ep.IsLocalSocket()
			if got != tt.want {
				t.Errorf("IsLocalSocket() = %v, want %v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/portainer/ -run TestEndpoint_IsLocalSocket -v`
Expected: FAIL — `IsLocalSocket` undefined

- [ ] **Step 3: Implement IsLocalSocket**

Add to `internal/portainer/types.go`:

```go
// IsLocalSocket returns true if this endpoint connects via the local Docker socket.
// These endpoints duplicate what Sentinel monitors directly and should be blocked.
func (e Endpoint) IsLocalSocket() bool {
	if strings.HasPrefix(e.URL, "unix://") {
		return true
	}
	// Empty URL with Docker environment type means local socket mount.
	// Agent (type 2) and Edge (type 4) endpoints always have a URL.
	return e.URL == "" && e.Type == EndpointDocker
}
```

Add `"strings"` to imports.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/portainer/ -run TestEndpoint_IsLocalSocket -v`
Expected: All PASS

- [ ] **Step 5: Commit**

```bash
cd /home/lns/Docker-Sentinel
git add internal/portainer/types.go internal/portainer/types_test.go
git commit -m "feat: add IsLocalSocket detection for Portainer endpoints"
```

---

## Chunk 4: Web API — Instance CRUD Endpoints

### Task 6: New API Routes and Handlers

**Files:**
- Modify: `internal/web/api_portainer.go` (rewrite for multi-instance)
- Modify: `internal/web/server.go:486-492` (update route registrations)
- Modify: `internal/web/interfaces.go:330-336` (update PortainerProvider interface)
- Modify: `internal/web/interfaces.go:387-406` (add InstanceID fields to types)
- Modify: `internal/web/api_config.go:38-93` (update valid/sensitive key lists)

- [ ] **Step 1: Update PortainerProvider interface**

In `internal/web/interfaces.go`, update the interface (lines 330-336):

```go
// PortainerProvider provides multi-instance Portainer access for the web layer.
type PortainerProvider interface {
	TestConnection(ctx context.Context, instanceID string) error
	Endpoints(ctx context.Context, instanceID string) ([]PortainerEndpoint, error)
	AllEndpoints(ctx context.Context, instanceID string) ([]PortainerEndpoint, error)
	EndpointContainers(ctx context.Context, instanceID string, endpointID int) ([]PortainerContainerInfo, error)
}
```

Add `Type` field to `PortainerEndpoint` (needed for IsLocalSocket detection in UI):

```go
type PortainerEndpoint struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Type       int    `json:"type"`
	Status     string `json:"status"`
	InstanceID string `json:"instance_id,omitempty"`
}
```

Add `InstanceID` and `InstanceName` fields to `PortainerContainerInfo`:

```go
type PortainerContainerInfo struct {
	// ... existing fields ...
	InstanceID   string `json:"instance_id,omitempty"`
	InstanceName string `json:"instance_name,omitempty"`
}
```

Update `convertPortainerEndpoints` in `cmd/sentinel/adapters_cluster.go:317-332` to map `Type: int(ep.Type)`.

**All callers of the old PortainerProvider interface that need updating:**
- `internal/web/api_portainer.go:25` — `AllEndpoints(r.Context())`
- `internal/web/api_portainer.go:47` — `EndpointContainers(r.Context(), endpointID)`
- `internal/web/handlers.go:~510` — container detail page `Portainer.EndpointContainers`
- `internal/web/handlers_dashboard.go` — `withPortainer` helper
- `cmd/sentinel/adapters_cluster.go:253-332` — `portainerAdapter` (replaced by `multiPortainerAdapter`)

- [ ] **Step 2: Add PortainerInstanceStore interface**

In `internal/web/interfaces.go`, add:

```go
// PortainerInstanceStore persists Portainer instance configuration.
type PortainerInstanceStore interface {
	ListPortainerInstances() ([]PortainerInstanceConfig, error)
	GetPortainerInstance(id string) (PortainerInstanceConfig, error)
	SavePortainerInstance(inst PortainerInstanceConfig) error
	DeletePortainerInstance(id string) error
	NextPortainerID() (string, error)
}

// PortainerInstanceConfig mirrors store.PortainerInstance for the web layer.
type PortainerInstanceConfig struct {
	ID        string                      `json:"id"`
	Name      string                      `json:"name"`
	URL       string                      `json:"url"`
	Token     string                      `json:"token,omitempty"`
	Enabled   bool                        `json:"enabled"`
	Endpoints map[string]EndpointCfg      `json:"endpoints,omitempty"`
}

// EndpointCfg mirrors store.EndpointConfig.
type EndpointCfg struct {
	Enabled bool   `json:"enabled"`
	Blocked bool   `json:"blocked,omitempty"`
	Reason  string `json:"reason,omitempty"`
}
```

- [ ] **Step 3: Add PortainerInstanceStore to Dependencies**

In `internal/web/server.go`, add to Dependencies (after the Portainer line):

```go
PortainerInstances PortainerInstanceStore
```

- [ ] **Step 4: Rewrite api_portainer.go for multi-instance**

Replace `internal/web/api_portainer.go` with new multi-instance handlers:

| Method | Path | Handler | Purpose |
|--------|------|---------|---------|
| GET | `/api/portainer/instances` | `apiListPortainerInstances` | List all instances (token redacted) |
| POST | `/api/portainer/instances` | `apiCreatePortainerInstance` | Add new instance |
| PUT | `/api/portainer/instances/{id}` | `apiUpdatePortainerInstance` | Edit name/url/token/enabled |
| DELETE | `/api/portainer/instances/{id}` | `apiDeletePortainerInstance` | Remove instance |
| POST | `/api/portainer/instances/{id}/test` | `apiTestPortainerInstance` | Test connection, detect local sockets, save endpoints |
| GET | `/api/portainer/instances/{id}/endpoints` | `apiPortainerInstanceEndpoints` | List endpoints for instance |
| PUT | `/api/portainer/instances/{id}/endpoints/{epid}` | `apiUpdatePortainerEndpoint` | Toggle endpoint enabled |
| GET | `/api/portainer/endpoints/{id}/containers` | Keep existing (adapted) | Containers for endpoint |

The test-connection handler (`apiTestPortainerInstance`) should:
1. Load instance config from store
2. Create a temp `portainer.Client` from the instance's URL + token
3. Call `TestConnection(ctx)`
4. On success, call `AllEndpoints(ctx)` to get the endpoint list
5. Run `IsLocalSocket()` on each endpoint to set blocked/reason
6. Save endpoint configs back to the instance in the store
7. Wire the scanner into the engine and web adapter (hot-reload)
8. Return the endpoint list with blocked status

- [ ] **Step 5: Update route registrations in server.go**

Replace the old Portainer routes (lines 486-492) with the new multi-instance routes:

```go
// Portainer multi-instance.
s.mux.Handle("GET /portainer", perm(auth.PermSettingsModify, s.handlePortainer))
s.mux.Handle("GET /api/portainer/instances", perm(auth.PermSettingsModify, s.apiListPortainerInstances))
s.mux.Handle("POST /api/portainer/instances", perm(auth.PermSettingsModify, s.apiCreatePortainerInstance))
s.mux.Handle("PUT /api/portainer/instances/{id}", perm(auth.PermSettingsModify, s.apiUpdatePortainerInstance))
s.mux.Handle("DELETE /api/portainer/instances/{id}", perm(auth.PermSettingsModify, s.apiDeletePortainerInstance))
s.mux.Handle("POST /api/portainer/instances/{id}/test", perm(auth.PermSettingsModify, s.apiTestPortainerInstance))
s.mux.Handle("GET /api/portainer/instances/{id}/endpoints", perm(auth.PermContainersView, s.apiPortainerInstanceEndpoints))
s.mux.Handle("PUT /api/portainer/instances/{id}/endpoints/{epid}", perm(auth.PermSettingsModify, s.apiUpdatePortainerEndpoint))
s.mux.Handle("GET /api/portainer/endpoints/{id}/containers", perm(auth.PermContainersView, s.apiPortainerContainers))
```

- [ ] **Step 6: Update api_config.go valid/sensitive key lists**

Remove the old Portainer keys from `validSettingKeys` (lines 91-93) and `sensitiveKeys` (line 39). These are no longer stored as flat settings. The old setting constants in `bolt.go` (lines 52-57) can be kept temporarily for migration but should not be in the valid keys list.

- [ ] **Step 7: Run tests**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/web/ -v -count=1`
Expected: PASS (may need adapter updates; see Task 7)

- [ ] **Step 8: Commit**

```bash
cd /home/lns/Docker-Sentinel
git add internal/web/
git commit -m "feat: multi-instance Portainer API endpoints with local socket detection"
```

### Task 7: Update Adapters in main.go

**Files:**
- Modify: `cmd/sentinel/adapters_cluster.go:253-400` (multi-instance portainer adapters)
- Modify: `cmd/sentinel/main.go:601-622,746-770` (multi-instance init + factory)

- [ ] **Step 1: Create multi-instance portainer web adapter**

In `cmd/sentinel/adapters_cluster.go`, replace `portainerAdapter` with a multi-instance wrapper:

```go
// multiPortainerAdapter bridges multiple portainer.Scanner instances to web.PortainerProvider.
type multiPortainerAdapter struct {
	mu       sync.RWMutex
	scanners map[string]*portainer.Scanner // keyed by instance ID
}

func newMultiPortainerAdapter() *multiPortainerAdapter {
	return &multiPortainerAdapter{scanners: make(map[string]*portainer.Scanner)}
}

func (a *multiPortainerAdapter) Set(id string, scanner *portainer.Scanner) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scanners[id] = scanner
}

func (a *multiPortainerAdapter) Remove(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.scanners, id)
}
```

Then implement `TestConnection`, `Endpoints`, `AllEndpoints`, `EndpointContainers` methods that look up the scanner by instanceID and delegate.

- [ ] **Step 2: Create portainerInstanceStoreAdapter**

In `cmd/sentinel/adapters_cluster.go`, add an adapter bridging `store.Store` to `web.PortainerInstanceStore`:

```go
type portainerInstanceStoreAdapter struct {
	store *store.Store
}

func (a *portainerInstanceStoreAdapter) ListPortainerInstances() ([]web.PortainerInstanceConfig, error) {
	// Load from store, convert store types to web types.
}

func (a *portainerInstanceStoreAdapter) GetPortainerInstance(id string) (web.PortainerInstanceConfig, error) { ... }
func (a *portainerInstanceStoreAdapter) SavePortainerInstance(inst web.PortainerInstanceConfig) error { ... }
func (a *portainerInstanceStoreAdapter) DeletePortainerInstance(id string) error { ... }
func (a *portainerInstanceStoreAdapter) NextPortainerID() (string, error) { ... }
```

- [ ] **Step 3: Update main.go Portainer initialization**

Replace the single-instance init block (lines 601-622) with multi-instance init:

1. Call `db.MigratePortainerSettings()` + `db.MigratePortainerKeys("p1")` on boot
2. Load all instances via `db.ListPortainerInstances()`
3. For each enabled instance, create Client + Scanner, register in adapter and engine
4. Call `updater.SetPortainerInstances(engineInstances)`

- [ ] **Step 4: Update web deps wiring**

Replace `webDeps.Portainer` and `PortainerInitFunc` (lines 746-770) with:

```go
webDeps.Portainer = portainerAdapter
webDeps.PortainerInstances = &portainerInstanceStoreAdapter{store: db}
```

The `PortainerInitFunc` factory is no longer needed since instance creation/testing is handled by the per-instance test endpoint in the API.

- [ ] **Step 5: Run full test suite**

Run: `cd /home/lns/Docker-Sentinel && go test ./... -count=1`
Expected: All PASS

- [ ] **Step 6: Run linter**

Run: `cd /home/lns/Docker-Sentinel && make lint`
Expected: No issues

- [ ] **Step 7: Commit**

```bash
cd /home/lns/Docker-Sentinel
git add cmd/sentinel/ internal/
git commit -m "feat: wire multi-instance Portainer adapters and migration on boot"
```

---

## Chunk 5: Dashboard Integration — Portainer Host Groups

### Task 8: Add Portainer Host Groups to Dashboard Handler

**Files:**
- Modify: `internal/web/handlers_dashboard.go:631-757` (add Portainer groups after cluster groups)

- [ ] **Step 1: Enable host groups when Portainer instances exist**

The `data.HostGroups` list is currently only populated when `s.deps.Cluster != nil && s.deps.Cluster.Enabled()`. Update the guard condition:

```go
// Helper to check if any Portainer instances are enabled with usable endpoints.
func (s *Server) hasEnabledPortainerInstances() bool {
    if s.deps.PortainerInstances == nil {
        return false
    }
    instances, err := s.deps.PortainerInstances.ListPortainerInstances()
    if err != nil {
        return false
    }
    for _, inst := range instances {
        if !inst.Enabled {
            continue
        }
        for _, ep := range inst.Endpoints {
            if ep.Enabled && !ep.Blocked {
                return true
            }
        }
    }
    return false
}

// In the dashboard handler:
hasMultiHost := (s.deps.Cluster != nil && s.deps.Cluster.Enabled()) ||
    s.hasEnabledPortainerInstances()

if hasMultiHost {
    // Build local group (unchanged).
    localGroup := hostGroup{...}
    data.HostGroups = []hostGroup{localGroup}

    // Cluster groups (existing code, if cluster enabled).
    if s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
        // ... existing cluster host group code ...
    }

    // Portainer groups (new).
    s.appendPortainerHostGroups(r.Context(), &data)
}
```

- [ ] **Step 2: Implement appendPortainerHostGroups helper**

Extract the Portainer host group logic into a method on Server to keep the handler clean:

```go
func (s *Server) appendPortainerHostGroups(ctx context.Context, data *dashboardData) {
	if s.deps.PortainerInstances == nil || s.deps.Portainer == nil {
		return
	}
	instances, err := s.deps.PortainerInstances.ListPortainerInstances()
	if err != nil || len(instances) == 0 {
		return
	}

	for _, inst := range instances {
		if !inst.Enabled {
			continue
		}

		// Fetch endpoint names once per instance (avoid N+1 queries).
		epNames := make(map[int]string)
		if eps, err := s.deps.Portainer.AllEndpoints(ctx, inst.ID); err == nil {
			for _, ep := range eps {
				epNames[ep.ID] = ep.Name
			}
		}

		for epIDStr, epCfg := range inst.Endpoints {
			if epCfg.Blocked || !epCfg.Enabled {
				continue
			}
			epID, _ := strconv.Atoi(epIDStr)
			containers, err := s.deps.Portainer.EndpointContainers(ctx, inst.ID, epID)
			if err != nil {
				continue
			}

			hostID := fmt.Sprintf("portainer:%s:%s", inst.ID, epIDStr)
			var views []containerView

			for _, c := range containers {
				// Build containerView using same pattern as cluster remote containers:
				// tag extraction, policy resolution, queue lookup, severity classification.
				// Use hostID for scoped key lookups.
			}

			if len(views) > 0 {
				sg := stackGroup{Name: "Standalone", Containers: views}
				for _, c := range views {
					if c.State == "running" { sg.RunningCount++ } else { sg.StoppedCount++ }
					if c.HasUpdate { sg.HasPending = true; sg.PendingCount++ }
				}

				epName := epNames[epID]
				if epName == "" {
					epName = fmt.Sprintf("Endpoint %d", epID)
				}

				// Simplify name if only one endpoint on this instance.
				groupName := inst.Name + " / " + epName
				enabledCount := 0
				for _, cfg := range inst.Endpoints {
					if cfg.Enabled && !cfg.Blocked { enabledCount++ }
				}
				if enabledCount == 1 {
					groupName = inst.Name
				}

				data.HostGroups = append(data.HostGroups, hostGroup{
					ID:        hostID,
					Name:      groupName,
					Connected: true,
					Stacks:    []stackGroup{sg},
					Count:     len(views),
				})
				data.TotalContainers += len(views)
				for _, c := range views {
					if c.State == "running" { data.RunningContainers++ }
					if c.HasUpdate { data.PendingUpdates++ }
				}
			}
		}
	}
}
```

Consider extracting the container-to-containerView conversion (shared with cluster code) into a helper to avoid duplication.

- [ ] **Step 3: Run tests**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/web/ -run TestDashboard -v -count=1`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /home/lns/Docker-Sentinel
git add internal/web/handlers_dashboard.go
git commit -m "feat: Portainer containers appear as dashboard host groups"
```

### Task 9: Update Container Detail for New Key Format

**Files:**
- Modify: `internal/web/handlers.go` (update `portainer:` prefix parsing)

- [ ] **Step 1: Update handleContainerDetail**

The current code parses `host=portainer:3`. Update to handle `host=portainer:p1:3`:

```go
if strings.HasPrefix(hostFilter, "portainer:") && s.deps.Portainer != nil {
    // Parse "portainer:p1:3" -> instanceID="p1", endpointID=3
    parts := strings.SplitN(strings.TrimPrefix(hostFilter, "portainer:"), ":", 2)
    var instanceID, epIDStr string
    if len(parts) == 2 {
        instanceID = parts[0]
        epIDStr = parts[1]
    } else {
        // Backwards compat: "portainer:3" (no instance ID).
        epIDStr = parts[0]
    }
    epID, err := strconv.Atoi(epIDStr)
    if err != nil {
        // ... error handling
    }
    containers, err := s.deps.Portainer.EndpointContainers(r.Context(), instanceID, epID)
    // ... rest unchanged
}
```

- [ ] **Step 2: Run tests**

Run: `cd /home/lns/Docker-Sentinel && go test ./internal/web/ -v -count=1`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
cd /home/lns/Docker-Sentinel
git add internal/web/handlers.go
git commit -m "fix: container detail page supports new portainer:ID:EP key format"
```

---

## Chunk 6: Frontend — Connectors Page Redesign

### Task 10: Connectors Page Instance Cards

**Files:**
- Modify: `internal/web/static/connectors.html` (replace single form with instance card list)

- [ ] **Step 1: Replace the Portainer tab HTML**

Replace lines 78-124 of `connectors.html` with a card list layout:

The new structure should contain:
- A container div `#portainer-instances` populated by JS on page load
- An "Add Instance" button at the bottom
- Each instance rendered as a card with: name input, URL input, token password input, test button, endpoint list area, remove button

Use DOM creation methods (createElement/appendChild) rather than string templates to avoid XSS risks. The `renderInstanceCard` function should build cards programmatically.

- [ ] **Step 2: Replace the Portainer JS functions**

Replace the inline `<script>` Portainer functions (lines 245-305) with multi-instance equivalents:

Key functions to implement:
- `loadPortainerInstances()` - GET `/api/portainer/instances`, render cards
- `addPortainerInstance()` - POST `/api/portainer/instances`, append card
- `savePortainerInstance(id)` - PUT `/api/portainer/instances/{id}`
- `testPortainerInstance(id)` - POST `/api/portainer/instances/{id}/test`, render endpoints
- `removePortainerInstance(id)` - DELETE with confirmation dialog
- `togglePortainerEndpoint(instanceId, endpointId, enabled)` - PUT endpoint toggle
- `renderInstanceCard(container, instance)` - Build card DOM elements
- `renderEndpoints(cardEl, endpoints)` - Show endpoint toggles, greyed-out blocked ones

- [ ] **Step 3: Update the settings loader**

In the page's init function, replace the old Portainer settings load with `loadPortainerInstances()`.

- [ ] **Step 4: Build frontend**

Run: `cd /home/lns/Docker-Sentinel && make frontend`
Expected: Builds successfully

- [ ] **Step 5: Commit**

```bash
cd /home/lns/Docker-Sentinel
git add internal/web/static/
git commit -m "feat: multi-instance Portainer connector UI with endpoint toggles"
```

---

## Chunk 7: Build, Deploy, Verify

### Task 11: Full Build and Lint

- [ ] **Step 1: Run full test suite**

Run: `cd /home/lns/Docker-Sentinel && go test ./... -count=1`
Expected: All PASS

- [ ] **Step 2: Run linter**

Run: `cd /home/lns/Docker-Sentinel && make lint`
Expected: Clean

- [ ] **Step 3: Build frontend**

Run: `cd /home/lns/Docker-Sentinel && make frontend`
Expected: Clean build

- [ ] **Step 4: Build Docker image**

Run: `cd /home/lns/Docker-Sentinel && docker build -t docker-sentinel:test .`
Expected: Successful build

### Task 12: Deploy and Verify

- [ ] **Step 1: Deploy using `/lucknet-ops:deploy-sentinel`**

Tag as the next version (check CHANGELOG for current).

- [ ] **Step 2: Verify migration**

Check Docker logs for migration message: "migrated old portainer settings to instance record"

- [ ] **Step 3: Verify connectors page**

Navigate to connectors page. The old single form should now show the migrated instance as a card named "Portainer" with the existing URL and token.

- [ ] **Step 4: Verify dashboard**

Portainer endpoint containers should appear as host groups on the dashboard.

- [ ] **Step 5: Test adding a second instance**

Add a second Portainer instance via the connectors page. Test connection. Verify endpoints populate with local socket detection.

### Task 13: Update Changelog and Documentation

- [ ] **Step 1: Update CHANGELOG.md**

Add a new version entry with:
- **Added:** Multi-instance Portainer support, local socket detection, Portainer containers on dashboard
- **Changed:** Connectors page redesigned for instance cards, queue key format includes instance ID
- **Migration:** Old single-instance Portainer settings auto-migrate to new format

- [ ] **Step 2: Commit documentation**

```bash
cd /home/lns/Docker-Sentinel
git add CHANGELOG.md
git commit -m "docs: add multi-instance Portainer to changelog"
```
