package registry

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestDiscover(t *testing.T) {
	tracker := NewRateLimitTracker()
	tracker.Discover("ghcr.io", 3)

	statuses := tracker.Status()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 registry, got %d", len(statuses))
	}
	s := statuses[0]
	if s.Registry != "ghcr.io" {
		t.Errorf("expected registry %q, got %q", "ghcr.io", s.Registry)
	}
	if s.ContainerCount != 3 {
		t.Errorf("expected container count 3, got %d", s.ContainerCount)
	}
	if s.Limit != -1 {
		t.Errorf("expected Limit -1 (unknown), got %d", s.Limit)
	}
}

func TestDiscoverUpdatesCount(t *testing.T) {
	tracker := NewRateLimitTracker()
	tracker.Discover("ghcr.io", 2)
	tracker.Discover("ghcr.io", 5)

	statuses := tracker.Status()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 registry, got %d", len(statuses))
	}
	if statuses[0].ContainerCount != 5 {
		t.Errorf("expected container count 5, got %d", statuses[0].ContainerCount)
	}
}

func TestRecordDockerHubHeaders(t *testing.T) {
	tracker := NewRateLimitTracker()
	before := time.Now()

	h := make(http.Header)
	h.Set("RateLimit-Limit", "100;w=21600")
	h.Set("RateLimit-Remaining", "87;w=21600")
	tracker.Record("docker.io", h)

	statuses := tracker.Status()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 registry, got %d", len(statuses))
	}
	s := statuses[0]
	if !s.HasLimits {
		t.Error("expected HasLimits=true")
	}
	if s.Limit != 100 {
		t.Errorf("expected Limit=100, got %d", s.Limit)
	}
	if s.Remaining != 87 {
		t.Errorf("expected Remaining=87, got %d", s.Remaining)
	}
	// ResetAt should be approximately 21600 seconds from now.
	expectedReset := before.Add(21600 * time.Second)
	if s.ResetAt.Before(before) {
		t.Errorf("expected ResetAt in the future, got %v (before %v)", s.ResetAt, before)
	}
	// Allow 5 seconds of tolerance for test execution time.
	if s.ResetAt.After(expectedReset.Add(5 * time.Second)) {
		t.Errorf("expected ResetAt around %v, got %v", expectedReset, s.ResetAt)
	}
}

func TestRecordGHCRHeaders(t *testing.T) {
	tracker := NewRateLimitTracker()
	resetEpoch := time.Now().Add(1 * time.Hour).Unix()

	h := make(http.Header)
	h.Set("X-RateLimit-Limit", "5000")
	h.Set("X-RateLimit-Remaining", "4999")
	h.Set("X-RateLimit-Reset", fmt.Sprintf("%d", resetEpoch))
	tracker.Record("ghcr.io", h)

	statuses := tracker.Status()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 registry, got %d", len(statuses))
	}
	s := statuses[0]
	if !s.HasLimits {
		t.Error("expected HasLimits=true")
	}
	if s.Limit != 5000 {
		t.Errorf("expected Limit=5000, got %d", s.Limit)
	}
	if s.Remaining != 4999 {
		t.Errorf("expected Remaining=4999, got %d", s.Remaining)
	}
	if s.ResetAt.Unix() != resetEpoch {
		t.Errorf("expected ResetAt epoch %d, got %d", resetEpoch, s.ResetAt.Unix())
	}
}

func TestRecordNoHeaders(t *testing.T) {
	tracker := NewRateLimitTracker()
	tracker.Record("docker.io", make(http.Header))

	statuses := tracker.Status()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 registry, got %d", len(statuses))
	}
	if statuses[0].HasLimits {
		t.Error("expected HasLimits=false when no rate limit headers present")
	}
}

func TestRecordAutoDiscovers(t *testing.T) {
	tracker := NewRateLimitTracker()

	// Record on a registry that was never Discovered.
	h := make(http.Header)
	h.Set("RateLimit-Limit", "100;w=21600")
	h.Set("RateLimit-Remaining", "99;w=21600")
	tracker.Record("docker.io", h)

	statuses := tracker.Status()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 registry, got %d", len(statuses))
	}
	if statuses[0].Registry != "docker.io" {
		t.Errorf("expected registry %q, got %q", "docker.io", statuses[0].Registry)
	}
	if !statuses[0].HasLimits {
		t.Error("expected HasLimits=true after recording headers")
	}
}

func TestCanProceedUnknown(t *testing.T) {
	tracker := NewRateLimitTracker()

	ok, wait := tracker.CanProceed("totally-unknown.io", 10)
	if !ok {
		t.Error("expected CanProceed=true for unknown registry")
	}
	if wait != 0 {
		t.Errorf("expected wait=0, got %v", wait)
	}
}

func TestCanProceedNoLimits(t *testing.T) {
	tracker := NewRateLimitTracker()
	tracker.Discover("docker.io", 1)
	// Record with no rate limit headers.
	tracker.Record("docker.io", make(http.Header))

	ok, wait := tracker.CanProceed("docker.io", 10)
	if !ok {
		t.Error("expected CanProceed=true when no rate limits detected")
	}
	if wait != 0 {
		t.Errorf("expected wait=0, got %v", wait)
	}
}

func TestCanProceedAboveReserve(t *testing.T) {
	tracker := NewRateLimitTracker()

	h := make(http.Header)
	h.Set("RateLimit-Limit", "100;w=21600")
	h.Set("RateLimit-Remaining", "50;w=21600")
	tracker.Record("docker.io", h)

	ok, wait := tracker.CanProceed("docker.io", 10)
	if !ok {
		t.Error("expected CanProceed=true when Remaining(50) > reserve(10)")
	}
	if wait != 0 {
		t.Errorf("expected wait=0, got %v", wait)
	}
}

func TestCanProceedBelowReserve(t *testing.T) {
	tracker := NewRateLimitTracker()

	h := make(http.Header)
	h.Set("RateLimit-Limit", "100;w=21600")
	h.Set("RateLimit-Remaining", "5;w=21600")
	tracker.Record("docker.io", h)

	ok, wait := tracker.CanProceed("docker.io", 10)
	if ok {
		t.Error("expected CanProceed=false when Remaining(5) <= reserve(10)")
	}
	if wait <= 0 {
		t.Errorf("expected positive wait duration, got %v", wait)
	}
}

func TestCanProceedStaleReset(t *testing.T) {
	tracker := NewRateLimitTracker()

	// Record with headers that produce a future ResetAt.
	h := make(http.Header)
	h.Set("RateLimit-Limit", "100;w=21600")
	h.Set("RateLimit-Remaining", "5;w=21600")
	tracker.Record("docker.io", h)

	// Verify it's blocked first.
	ok, _ := tracker.CanProceed("docker.io", 10)
	if ok {
		t.Fatal("precondition failed: expected CanProceed=false before stale manipulation")
	}

	// Directly set ResetAt to the past to simulate stale data.
	// Access through the tracker's internal map (same package = internal test).
	tracker.mu.Lock()
	state := tracker.registries["docker.io"]
	state.ResetAt = time.Now().Add(-1 * time.Hour)
	tracker.mu.Unlock()

	ok, wait := tracker.CanProceed("docker.io", 10)
	if !ok {
		t.Error("expected CanProceed=true when ResetAt is in the past (stale)")
	}
	if wait != 0 {
		t.Errorf("expected wait=0 for stale reset, got %v", wait)
	}
}

func TestCanProceedDecrementsRemaining(t *testing.T) {
	tracker := NewRateLimitTracker()

	h := make(http.Header)
	h.Set("RateLimit-Limit", "100;w=21600")
	h.Set("RateLimit-Remaining", "12;w=21600")
	tracker.Record("docker.io", h)

	// First call with reserve=10: 12 > 10, should proceed
	ok, _ := tracker.CanProceed("docker.io", 10)
	if !ok {
		t.Fatal("first CanProceed should succeed (12 > 10)")
	}
	// Second call: effective remaining is 11, still > 10
	ok, _ = tracker.CanProceed("docker.io", 10)
	if !ok {
		t.Fatal("second CanProceed should succeed (11 > 10)")
	}
	// Third call: effective remaining is 10, not > 10 — blocked
	ok, _ = tracker.CanProceed("docker.io", 10)
	if ok {
		t.Fatal("third CanProceed should fail (10 <= 10)")
	}
}

func TestRecordResetsRequestCount(t *testing.T) {
	tracker := NewRateLimitTracker()

	h := make(http.Header)
	h.Set("RateLimit-Limit", "100;w=21600")
	h.Set("RateLimit-Remaining", "12;w=21600")
	tracker.Record("docker.io", h)

	// Use up some quota
	tracker.CanProceed("docker.io", 10) // effective 11
	tracker.CanProceed("docker.io", 10) // effective 10 — blocked

	// Fresh Record should reset the counter
	h2 := make(http.Header)
	h2.Set("RateLimit-Limit", "100;w=21600")
	h2.Set("RateLimit-Remaining", "50;w=21600")
	tracker.Record("docker.io", h2)

	ok, _ := tracker.CanProceed("docker.io", 10)
	if !ok {
		t.Fatal("after Record, CanProceed should succeed again (50 > 10)")
	}
}

func TestSetAuth(t *testing.T) {
	tracker := NewRateLimitTracker()
	tracker.Discover("ghcr.io", 2)
	tracker.SetAuth("ghcr.io", true)

	statuses := tracker.Status()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 registry, got %d", len(statuses))
	}
	if !statuses[0].IsAuth {
		t.Error("expected IsAuth=true after SetAuth(true)")
	}
}

func TestNormaliseRegistryHost(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"registry-1.docker.io", "docker.io"},
		{"index.docker.io", "docker.io"},
		{"docker.io", "docker.io"},
		{"ghcr.io", "ghcr.io"},
		{"hotio.dev", "hotio.dev"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormaliseRegistryHost(tt.input)
			if got != tt.want {
				t.Errorf("NormaliseRegistryHost(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestOverallHealthOK(t *testing.T) {
	tracker := NewRateLimitTracker()
	// A tracker with no rate-limited registries should report "ok".
	tracker.Discover("docker.io", 1)

	health := tracker.OverallHealth()
	if health != "ok" {
		t.Errorf("expected health %q, got %q", "ok", health)
	}
}

func TestOverallHealthLow(t *testing.T) {
	tracker := NewRateLimitTracker()

	// Record with Remaining at 15% of Limit (15 out of 100).
	h := make(http.Header)
	h.Set("RateLimit-Limit", "100;w=21600")
	h.Set("RateLimit-Remaining", "15;w=21600")
	tracker.Record("docker.io", h)

	health := tracker.OverallHealth()
	if health != "low" {
		t.Errorf("expected health %q, got %q", "low", health)
	}
}

func TestOverallHealthExhausted(t *testing.T) {
	tracker := NewRateLimitTracker()

	// Record with Remaining=0.
	h := make(http.Header)
	h.Set("RateLimit-Limit", "100;w=21600")
	h.Set("RateLimit-Remaining", "0;w=21600")
	tracker.Record("docker.io", h)

	health := tracker.OverallHealth()
	if health != "exhausted" {
		t.Errorf("expected health %q, got %q", "exhausted", health)
	}
}
