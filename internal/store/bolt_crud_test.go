package store

import (
	"fmt"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Policy Overrides
// ---------------------------------------------------------------------------

func TestPolicyOverrideRoundTrip(t *testing.T) {
	s := testStore(t)

	// No override initially.
	policy, ok := s.GetPolicyOverride("nginx")
	if ok || policy != "" {
		t.Errorf("expected (\"\", false), got (%q, %v)", policy, ok)
	}

	if err := s.SetPolicyOverride("nginx", "pinned"); err != nil {
		t.Fatal(err)
	}
	policy, ok = s.GetPolicyOverride("nginx")
	if !ok || policy != "pinned" {
		t.Errorf("expected (\"pinned\", true), got (%q, %v)", policy, ok)
	}
}

func TestPolicyOverrideOverwrite(t *testing.T) {
	s := testStore(t)

	if err := s.SetPolicyOverride("app", "auto"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPolicyOverride("app", "manual"); err != nil {
		t.Fatal(err)
	}

	policy, ok := s.GetPolicyOverride("app")
	if !ok || policy != "manual" {
		t.Errorf("expected (\"manual\", true), got (%q, %v)", policy, ok)
	}
}

func TestDeletePolicyOverride(t *testing.T) {
	s := testStore(t)

	if err := s.SetPolicyOverride("app", "pinned"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePolicyOverride("app"); err != nil {
		t.Fatal(err)
	}

	policy, ok := s.GetPolicyOverride("app")
	if ok || policy != "" {
		t.Errorf("expected (\"\", false) after delete, got (%q, %v)", policy, ok)
	}
}

func TestDeletePolicyOverrideNonexistent(t *testing.T) {
	s := testStore(t)

	// Deleting a key that doesn't exist should not error.
	if err := s.DeletePolicyOverride("ghost"); err != nil {
		t.Fatal(err)
	}
}

func TestAllPolicyOverrides(t *testing.T) {
	s := testStore(t)

	all := s.AllPolicyOverrides()
	if len(all) != 0 {
		t.Fatalf("expected empty map, got %v", all)
	}

	if err := s.SetPolicyOverride("nginx", "auto"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPolicyOverride("redis", "pinned"); err != nil {
		t.Fatal(err)
	}

	all = s.AllPolicyOverrides()
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
	if all["nginx"] != "auto" {
		t.Errorf("nginx = %q, want %q", all["nginx"], "auto")
	}
	if all["redis"] != "pinned" {
		t.Errorf("redis = %q, want %q", all["redis"], "pinned")
	}
}

// ---------------------------------------------------------------------------
// Logging
// ---------------------------------------------------------------------------

func TestAppendLogAutoTimestamp(t *testing.T) {
	s := testStore(t)

	entry := LogEntry{
		Type:    "update",
		Message: "updated nginx",
	}
	if err := s.AppendLog(entry); err != nil {
		t.Fatal(err)
	}

	logs, err := s.ListLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Timestamp.IsZero() {
		t.Error("expected auto-set timestamp, got zero")
	}
	if logs[0].Type != "update" {
		t.Errorf("type = %q, want %q", logs[0].Type, "update")
	}
	if logs[0].Message != "updated nginx" {
		t.Errorf("message = %q, want %q", logs[0].Message, "updated nginx")
	}
}

func TestAppendLogExplicitTimestamp(t *testing.T) {
	s := testStore(t)

	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	entry := LogEntry{
		Timestamp: ts,
		Type:      "policy_set",
		Message:   "set policy",
		Container: "redis",
		User:      "admin",
	}
	if err := s.AppendLog(entry); err != nil {
		t.Fatal(err)
	}

	logs, err := s.ListLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if !logs[0].Timestamp.Equal(ts) {
		t.Errorf("timestamp = %v, want %v", logs[0].Timestamp, ts)
	}
	if logs[0].Container != "redis" {
		t.Errorf("container = %q, want %q", logs[0].Container, "redis")
	}
	if logs[0].User != "admin" {
		t.Errorf("user = %q, want %q", logs[0].User, "admin")
	}
}

func TestListLogsNewestFirst(t *testing.T) {
	s := testStore(t)

	for i := 0; i < 5; i++ {
		entry := LogEntry{
			Timestamp: time.Date(2025, 1, 1, 0, i, 0, 0, time.UTC),
			Type:      "update",
			Message:   fmt.Sprintf("entry-%d", i),
		}
		if err := s.AppendLog(entry); err != nil {
			t.Fatal(err)
		}
	}

	logs, err := s.ListLogs(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(logs))
	}
	if logs[0].Message != "entry-4" {
		t.Errorf("first log = %q, want %q", logs[0].Message, "entry-4")
	}
	if logs[2].Message != "entry-2" {
		t.Errorf("third log = %q, want %q", logs[2].Message, "entry-2")
	}
}

func TestListLogsEmpty(t *testing.T) {
	s := testStore(t)

	logs, err := s.ListLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 {
		t.Errorf("expected 0 logs, got %d", len(logs))
	}
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

func TestSettingsRoundTrip(t *testing.T) {
	s := testStore(t)

	val, err := s.LoadSetting("theme")
	if err != nil {
		t.Fatal(err)
	}
	if val != "" {
		t.Errorf("expected empty string for missing setting, got %q", val)
	}

	if err := s.SaveSetting("theme", "dark"); err != nil {
		t.Fatal(err)
	}
	val, err = s.LoadSetting("theme")
	if err != nil {
		t.Fatal(err)
	}
	if val != "dark" {
		t.Errorf("expected %q, got %q", "dark", val)
	}
}

func TestSettingsOverwrite(t *testing.T) {
	s := testStore(t)

	if err := s.SaveSetting("key", "val1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSetting("key", "val2"); err != nil {
		t.Fatal(err)
	}

	val, err := s.LoadSetting("key")
	if err != nil {
		t.Fatal(err)
	}
	if val != "val2" {
		t.Errorf("expected %q, got %q", "val2", val)
	}
}

func TestVersionScopeRoundTrip(t *testing.T) {
	s := testStore(t)

	if scope := s.VersionScope(); scope != "" {
		t.Errorf("expected empty default scope, got %q", scope)
	}

	if err := s.SetVersionScope("default"); err != nil {
		t.Fatal(err)
	}
	if scope := s.VersionScope(); scope != "default" {
		t.Errorf("expected %q, got %q", "default", scope)
	}
}

func TestGetAllSettingsExcludesInternal(t *testing.T) {
	s := testStore(t)

	if err := s.SaveSetting("theme", "dark"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSetting("poll_interval", "6h"); err != nil {
		t.Fatal(err)
	}
	// Save the internal keys that should be excluded.
	if err := s.SaveSetting("notification_config", `{"gotify_url":"http://example.com"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSetting("notification_channels", `[{"id":"ch1"}]`); err != nil {
		t.Fatal(err)
	}

	all, err := s.GetAllSettings()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all["notification_config"]; ok {
		t.Error("notification_config should be excluded")
	}
	if _, ok := all["notification_channels"]; ok {
		t.Error("notification_channels should be excluded")
	}
	if all["theme"] != "dark" {
		t.Errorf("theme = %q, want %q", all["theme"], "dark")
	}
	if all["poll_interval"] != "6h" {
		t.Errorf("poll_interval = %q, want %q", all["poll_interval"], "6h")
	}
}

func TestGetAllSettingsEmpty(t *testing.T) {
	s := testStore(t)

	all, err := s.GetAllSettings()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("expected empty map, got %v", all)
	}
}

// ---------------------------------------------------------------------------
// Count Methods
// ---------------------------------------------------------------------------

func TestCountHistory(t *testing.T) {
	s := testStore(t)

	count, err := s.CountHistory()
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	for i := 0; i < 3; i++ {
		rec := UpdateRecord{
			Timestamp:     time.Now().UTC().Add(time.Duration(i) * time.Minute),
			ContainerName: fmt.Sprintf("app-%d", i),
			Outcome:       "success",
		}
		if err := s.RecordUpdate(rec); err != nil {
			t.Fatal(err)
		}
	}

	count, err = s.CountHistory()
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("expected 3, got %d", count)
	}
}

func TestCountSnapshots(t *testing.T) {
	s := testStore(t)

	count, err := s.CountSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	for i := 0; i < 4; i++ {
		if err := s.SaveSnapshot(fmt.Sprintf("svc-%d", i), []byte("data")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}

	count, err = s.CountSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Errorf("expected 4, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Scan Tracking
// ---------------------------------------------------------------------------

func TestContainerScanRoundTrip(t *testing.T) {
	s := testStore(t)

	got, err := s.GetLastContainerScan("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Errorf("expected zero time for unscanned container, got %v", got)
	}

	scanTime := time.Date(2025, 6, 15, 14, 30, 0, 0, time.UTC)
	if err := s.SetLastContainerScan("nginx", scanTime); err != nil {
		t.Fatal(err)
	}

	got, err = s.GetLastContainerScan("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(scanTime) {
		t.Errorf("expected %v, got %v", scanTime, got)
	}
}

func TestContainerScanIsolation(t *testing.T) {
	s := testStore(t)

	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	if err := s.SetLastContainerScan("app-a", t1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastContainerScan("app-b", t2); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetLastContainerScan("app-a")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(t1) {
		t.Errorf("app-a scan = %v, want %v", got, t1)
	}

	got, err = s.GetLastContainerScan("app-b")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(t2) {
		t.Errorf("app-b scan = %v, want %v", got, t2)
	}
}

// ---------------------------------------------------------------------------
// Port Configuration
// ---------------------------------------------------------------------------

func TestPortConfigRoundTrip(t *testing.T) {
	s := testStore(t)

	got, err := s.GetPortConfig("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil for missing port config")
	}

	if err := s.SetPortOverride("nginx", 8080, PortOverride{URL: "http://nginx.local"}); err != nil {
		t.Fatal(err)
	}

	got, err = s.GetPortConfig("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil port config")
	}
	if got.Ports["8080"].URL != "http://nginx.local" {
		t.Errorf("port 8080 URL = %q, want %q", got.Ports["8080"].URL, "http://nginx.local")
	}
}

func TestPortConfigMultiplePorts(t *testing.T) {
	s := testStore(t)

	if err := s.SetPortOverride("app", 80, PortOverride{URL: "http://app:80"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPortOverride("app", 443, PortOverride{URL: "https://app:443", Path: "/admin"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetPortConfig("app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(got.Ports))
	}
	if got.Ports["80"].URL != "http://app:80" {
		t.Errorf("port 80 URL = %q, want %q", got.Ports["80"].URL, "http://app:80")
	}
	if got.Ports["443"].Path != "/admin" {
		t.Errorf("port 443 Path = %q, want %q", got.Ports["443"].Path, "/admin")
	}
}

func TestPortConfigOverwritePort(t *testing.T) {
	s := testStore(t)

	if err := s.SetPortOverride("app", 80, PortOverride{URL: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPortOverride("app", 80, PortOverride{URL: "new"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetPortConfig("app")
	if err != nil {
		t.Fatal(err)
	}
	if got.Ports["80"].URL != "new" {
		t.Errorf("port 80 URL = %q, want %q", got.Ports["80"].URL, "new")
	}
}

func TestDeletePortOverride(t *testing.T) {
	s := testStore(t)

	if err := s.SetPortOverride("app", 80, PortOverride{URL: "http://app:80"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPortOverride("app", 443, PortOverride{URL: "https://app:443"}); err != nil {
		t.Fatal(err)
	}

	// Delete one port; the other should remain.
	if err := s.DeletePortOverride("app", 80); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetPortConfig("app")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil config (one port remains)")
	}
	if len(got.Ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(got.Ports))
	}
	if _, ok := got.Ports["80"]; ok {
		t.Error("port 80 should have been deleted")
	}
}

func TestDeletePortOverrideRemovesEntryWhenEmpty(t *testing.T) {
	s := testStore(t)

	if err := s.SetPortOverride("app", 80, PortOverride{URL: "url"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePortOverride("app", 80); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetPortConfig("app")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil when all ports deleted, got %+v", got)
	}
}

func TestDeletePortOverrideNonexistent(t *testing.T) {
	s := testStore(t)

	// Deleting from a container with no port config should not error.
	if err := s.DeletePortOverride("ghost", 80); err != nil {
		t.Fatal(err)
	}
}

func TestAllPortConfigs(t *testing.T) {
	s := testStore(t)

	if err := s.SetPortOverride("nginx", 80, PortOverride{URL: "http://nginx"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPortOverride("redis", 6379, PortOverride{Path: "/"}); err != nil {
		t.Fatal(err)
	}

	all, err := s.AllPortConfigs()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}
	if all["nginx"].Ports["80"].URL != "http://nginx" {
		t.Errorf("nginx port 80 URL = %q, want %q", all["nginx"].Ports["80"].URL, "http://nginx")
	}
	if all["redis"].Ports["6379"].Path != "/" {
		t.Errorf("redis port 6379 Path = %q, want %q", all["redis"].Ports["6379"].Path, "/")
	}
}

func TestAllPortConfigsEmpty(t *testing.T) {
	s := testStore(t)

	all, err := s.AllPortConfigs()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("expected empty map, got %v", all)
	}
}

// ---------------------------------------------------------------------------
// Hooks
// ---------------------------------------------------------------------------

func TestHookRoundTrip(t *testing.T) {
	s := testStore(t)

	hook := HookEntry{
		ContainerName: "nginx",
		Phase:         "pre-update",
		Command:       []string{"echo", "hello"},
		Timeout:       30,
	}
	if err := s.SaveHook(hook); err != nil {
		t.Fatal(err)
	}

	hooks, err := s.ListHooks("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooks))
	}
	if hooks[0].Phase != "pre-update" {
		t.Errorf("Phase = %q, want %q", hooks[0].Phase, "pre-update")
	}
	if hooks[0].Timeout != 30 {
		t.Errorf("Timeout = %d, want %d", hooks[0].Timeout, 30)
	}
	if len(hooks[0].Command) != 2 || hooks[0].Command[0] != "echo" {
		t.Errorf("Command = %v, want [echo hello]", hooks[0].Command)
	}
}

func TestHookMultiplePhases(t *testing.T) {
	s := testStore(t)

	pre := HookEntry{ContainerName: "app", Phase: "pre-update", Command: []string{"backup"}, Timeout: 60}
	post := HookEntry{ContainerName: "app", Phase: "post-update", Command: []string{"verify"}, Timeout: 10}
	if err := s.SaveHook(pre); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHook(post); err != nil {
		t.Fatal(err)
	}

	hooks, err := s.ListHooks("app")
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(hooks))
	}
}

func TestHookIsolation(t *testing.T) {
	s := testStore(t)

	h1 := HookEntry{ContainerName: "app-a", Phase: "pre-update", Command: []string{"cmd-a"}}
	h2 := HookEntry{ContainerName: "app-b", Phase: "pre-update", Command: []string{"cmd-b"}}
	if err := s.SaveHook(h1); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHook(h2); err != nil {
		t.Fatal(err)
	}

	hooks, err := s.ListHooks("app-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook for app-a, got %d", len(hooks))
	}
	if hooks[0].Command[0] != "cmd-a" {
		t.Errorf("Command = %v, want [cmd-a]", hooks[0].Command)
	}
}

func TestHookOverwrite(t *testing.T) {
	s := testStore(t)

	h1 := HookEntry{ContainerName: "app", Phase: "pre-update", Command: []string{"old"}, Timeout: 10}
	h2 := HookEntry{ContainerName: "app", Phase: "pre-update", Command: []string{"new"}, Timeout: 60}
	if err := s.SaveHook(h1); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHook(h2); err != nil {
		t.Fatal(err)
	}

	hooks, err := s.ListHooks("app")
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook (overwritten), got %d", len(hooks))
	}
	if hooks[0].Command[0] != "new" {
		t.Errorf("Command = %v, want [new]", hooks[0].Command)
	}
	if hooks[0].Timeout != 60 {
		t.Errorf("Timeout = %d, want %d", hooks[0].Timeout, 60)
	}
}

func TestDeleteHook(t *testing.T) {
	s := testStore(t)

	hook := HookEntry{ContainerName: "app", Phase: "pre-update", Command: []string{"cmd"}}
	if err := s.SaveHook(hook); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHook("app", "pre-update"); err != nil {
		t.Fatal(err)
	}

	hooks, err := s.ListHooks("app")
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 0 {
		t.Errorf("expected 0 hooks after delete, got %d", len(hooks))
	}
}

func TestListHooksEmpty(t *testing.T) {
	s := testStore(t)

	hooks, err := s.ListHooks("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 0 {
		t.Errorf("expected 0 hooks, got %d", len(hooks))
	}
}

// ---------------------------------------------------------------------------
// ListAllHistory
// ---------------------------------------------------------------------------

func TestListAllHistory(t *testing.T) {
	s := testStore(t)

	now := time.Now().UTC()
	records := []UpdateRecord{
		{Timestamp: now.Add(-2 * time.Minute), ContainerName: "nginx", Outcome: "success"},
		{Timestamp: now.Add(-1 * time.Minute), ContainerName: "redis", Outcome: "rollback"},
		{Timestamp: now, ContainerName: "postgres", Outcome: "success"},
	}
	for _, r := range records {
		if err := s.RecordUpdate(r); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListAllHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 records, got %d", len(got))
	}
	// Newest first.
	if got[0].ContainerName != "postgres" {
		t.Errorf("first = %q, want postgres", got[0].ContainerName)
	}
	if got[2].ContainerName != "nginx" {
		t.Errorf("last = %q, want nginx", got[2].ContainerName)
	}
}

func TestListAllHistoryEmpty(t *testing.T) {
	s := testStore(t)

	got, err := s.ListAllHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0, got %d", len(got))
	}
}
