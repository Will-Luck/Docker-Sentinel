package store

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Notification Templates
// ---------------------------------------------------------------------------

func TestNotifyTemplateRoundTrip(t *testing.T) {
	s := testStore(t)

	tmpl, err := s.GetNotifyTemplate("update")
	if err != nil {
		t.Fatal(err)
	}
	if tmpl != "" {
		t.Errorf("expected empty for missing template, got %q", tmpl)
	}

	if err := s.SaveNotifyTemplate("update", "Container {{.Name}} updated"); err != nil {
		t.Fatal(err)
	}

	tmpl, err = s.GetNotifyTemplate("update")
	if err != nil {
		t.Fatal(err)
	}
	if tmpl != "Container {{.Name}} updated" {
		t.Errorf("got %q, want %q", tmpl, "Container {{.Name}} updated")
	}
}

func TestDeleteNotifyTemplate(t *testing.T) {
	s := testStore(t)

	if err := s.SaveNotifyTemplate("rollback", "Rolled back {{.Name}}"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNotifyTemplate("rollback"); err != nil {
		t.Fatal(err)
	}

	tmpl, err := s.GetNotifyTemplate("rollback")
	if err != nil {
		t.Fatal(err)
	}
	if tmpl != "" {
		t.Errorf("expected empty after delete, got %q", tmpl)
	}
}

func TestGetAllNotifyTemplates(t *testing.T) {
	s := testStore(t)

	if err := s.SaveNotifyTemplate("update", "tmpl-update"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNotifyTemplate("rollback", "tmpl-rollback"); err != nil {
		t.Fatal(err)
	}

	all, err := s.GetAllNotifyTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}
	if all["update"] != "tmpl-update" {
		t.Errorf("update = %q, want %q", all["update"], "tmpl-update")
	}
	if all["rollback"] != "tmpl-rollback" {
		t.Errorf("rollback = %q, want %q", all["rollback"], "tmpl-rollback")
	}
}

func TestGetAllNotifyTemplatesEmpty(t *testing.T) {
	s := testStore(t)

	all, err := s.GetAllNotifyTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("expected empty map, got %v", all)
	}
}

// ---------------------------------------------------------------------------
// Notify State
// ---------------------------------------------------------------------------

func TestNotifyStateRoundTrip(t *testing.T) {
	s := testStore(t)

	got, err := s.GetNotifyState("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil for missing state")
	}

	now := time.Now().UTC().Truncate(time.Second)
	state := &NotifyState{
		LastDigest:   "sha256:abc123",
		LastNotified: now,
		FirstSeen:    now.Add(-time.Hour),
		SnoozedUntil: now.Add(24 * time.Hour),
	}
	if err := s.SetNotifyState("nginx", state); err != nil {
		t.Fatal(err)
	}

	got, err = s.GetNotifyState("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil state")
	}
	if got.LastDigest != "sha256:abc123" {
		t.Errorf("LastDigest = %q, want %q", got.LastDigest, "sha256:abc123")
	}
	if !got.LastNotified.Equal(now) {
		t.Errorf("LastNotified = %v, want %v", got.LastNotified, now)
	}
	if !got.FirstSeen.Equal(now.Add(-time.Hour)) {
		t.Errorf("FirstSeen = %v, want %v", got.FirstSeen, now.Add(-time.Hour))
	}
	if !got.SnoozedUntil.Equal(now.Add(24 * time.Hour)) {
		t.Errorf("SnoozedUntil = %v, want %v", got.SnoozedUntil, now.Add(24*time.Hour))
	}
}

func TestClearNotifyState(t *testing.T) {
	s := testStore(t)

	state := &NotifyState{LastDigest: "sha256:abc"}
	if err := s.SetNotifyState("app", state); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearNotifyState("app"); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetNotifyState("app")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil after clear, got %+v", got)
	}
}

func TestAllNotifyStates(t *testing.T) {
	s := testStore(t)

	s1 := &NotifyState{LastDigest: "sha256:aaa"}
	s2 := &NotifyState{LastDigest: "sha256:bbb"}
	if err := s.SetNotifyState("nginx", s1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNotifyState("redis", s2); err != nil {
		t.Fatal(err)
	}

	all, err := s.AllNotifyStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}
	if all["nginx"].LastDigest != "sha256:aaa" {
		t.Errorf("nginx.LastDigest = %q, want %q", all["nginx"].LastDigest, "sha256:aaa")
	}
	if all["redis"].LastDigest != "sha256:bbb" {
		t.Errorf("redis.LastDigest = %q, want %q", all["redis"].LastDigest, "sha256:bbb")
	}
}

func TestAllNotifyStatesEmpty(t *testing.T) {
	s := testStore(t)

	all, err := s.AllNotifyStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("expected empty map, got %v", all)
	}
}

// ---------------------------------------------------------------------------
// Notify Preferences
// ---------------------------------------------------------------------------

func TestNotifyPrefRoundTrip(t *testing.T) {
	s := testStore(t)

	got, err := s.GetNotifyPref("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil for missing pref")
	}

	pref := &NotifyPref{Mode: "every_scan"}
	if err := s.SetNotifyPref("nginx", pref); err != nil {
		t.Fatal(err)
	}

	got, err = s.GetNotifyPref("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil pref")
	}
	if got.Mode != "every_scan" {
		t.Errorf("Mode = %q, want %q", got.Mode, "every_scan")
	}
}

func TestDeleteNotifyPref(t *testing.T) {
	s := testStore(t)

	if err := s.SetNotifyPref("app", &NotifyPref{Mode: "muted"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNotifyPref("app"); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetNotifyPref("app")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

func TestAllNotifyPrefs(t *testing.T) {
	s := testStore(t)

	if err := s.SetNotifyPref("nginx", &NotifyPref{Mode: "digest_only"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNotifyPref("redis", &NotifyPref{Mode: "muted"}); err != nil {
		t.Fatal(err)
	}

	all, err := s.AllNotifyPrefs()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}
	if all["nginx"].Mode != "digest_only" {
		t.Errorf("nginx.Mode = %q, want %q", all["nginx"].Mode, "digest_only")
	}
	if all["redis"].Mode != "muted" {
		t.Errorf("redis.Mode = %q, want %q", all["redis"].Mode, "muted")
	}
}

func TestAllNotifyPrefsEmpty(t *testing.T) {
	s := testStore(t)

	all, err := s.AllNotifyPrefs()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("expected empty map, got %v", all)
	}
}

// ---------------------------------------------------------------------------
// Notification Config
// ---------------------------------------------------------------------------

func TestNotificationConfigRoundTrip(t *testing.T) {
	s := testStore(t)

	cfg, err := s.GetNotificationConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GotifyURL != "" || cfg.GotifyToken != "" || cfg.WebhookURL != "" {
		t.Errorf("expected zero value, got %+v", cfg)
	}

	want := NotificationConfig{
		GotifyURL:   "http://gotify:80",
		GotifyToken: "tok123",
		WebhookURL:  "http://hook.example.com",
		WebhookHeaders: map[string]string{
			"Authorization": "Bearer secret",
		},
	}
	if err := s.SetNotificationConfig(want); err != nil {
		t.Fatal(err)
	}

	cfg, err = s.GetNotificationConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GotifyURL != want.GotifyURL {
		t.Errorf("GotifyURL = %q, want %q", cfg.GotifyURL, want.GotifyURL)
	}
	if cfg.GotifyToken != want.GotifyToken {
		t.Errorf("GotifyToken = %q, want %q", cfg.GotifyToken, want.GotifyToken)
	}
	if cfg.WebhookURL != want.WebhookURL {
		t.Errorf("WebhookURL = %q, want %q", cfg.WebhookURL, want.WebhookURL)
	}
	if cfg.WebhookHeaders["Authorization"] != "Bearer secret" {
		t.Errorf("WebhookHeaders[Authorization] = %q, want %q", cfg.WebhookHeaders["Authorization"], "Bearer secret")
	}
}

func TestNotificationConfigOverwrite(t *testing.T) {
	s := testStore(t)

	if err := s.SetNotificationConfig(NotificationConfig{GotifyURL: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNotificationConfig(NotificationConfig{GotifyURL: "new"}); err != nil {
		t.Fatal(err)
	}

	cfg, err := s.GetNotificationConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GotifyURL != "new" {
		t.Errorf("GotifyURL = %q, want %q", cfg.GotifyURL, "new")
	}
}
