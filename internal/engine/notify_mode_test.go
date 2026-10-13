package engine

import (
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

func TestShouldNotifyUpdateEveryScan(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	seen := &store.NotifyState{LastDigest: "sha256:aaa", LastNotified: now.Add(-time.Hour)}

	if !shouldNotifyUpdate("every_scan", seen, "sha256:aaa", now) {
		t.Error("every_scan must notify again for a digest already notified about")
	}
	if shouldNotifyUpdate("default", seen, "sha256:aaa", now) {
		t.Error("default mode must stay silent for a digest already notified about")
	}
	if !shouldNotifyUpdate("default", seen, "sha256:bbb", now) {
		t.Error("default mode must notify for a new digest")
	}
	if shouldNotifyUpdate("muted", nil, "sha256:aaa", now) || shouldNotifyUpdate("digest_only", nil, "sha256:aaa", now) {
		t.Error("muted and digest_only must not send immediate notifications")
	}
}

func TestShouldNotifyUpdateSnooze(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	st := &store.NotifyState{LastDigest: "d", LastNotified: now.Add(-time.Hour), SnoozedUntil: now.Add(time.Hour)}
	if shouldNotifyUpdate("default", st, "d", now) {
		t.Error("snoozed container must stay silent")
	}
	st.SnoozedUntil = now.Add(-time.Minute)
	if !shouldNotifyUpdate("default", st, "d", now) {
		t.Error("expired snooze must notify again")
	}
}

func TestEveryScanContainersAreNotInTheDigest(t *testing.T) {
	if inScheduledDigest("every_scan") || inScheduledDigest("muted") {
		t.Error("every_scan and muted containers must be left out of the scheduled digest")
	}
	if !inScheduledDigest("default") || !inScheduledDigest("digest_only") {
		t.Error("default and digest_only containers belong in the digest")
	}
}

// Notified about digest A, then the tag moves to B while every channel is
// down: B must be retried on the next scan, not treated as already notified.
func TestFailedDeliveryOfNewDigestIsRetried(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	existing := &store.NotifyState{LastDigest: "A", LastNotified: now.Add(-24 * time.Hour)}

	// The scan that sees B and fails to deliver.
	saved := &store.NotifyState{LastDigest: "B", LastNotified: carriedLastNotified(existing, "B")}
	if !shouldNotifyUpdate("default", saved, "B", now) {
		t.Fatal("B was not retried: the old notification time suppressed it")
	}

	// The same digest keeps its record so it is not announced twice.
	if got := carriedLastNotified(existing, "A"); !got.Equal(existing.LastNotified) {
		t.Errorf("same digest lost its LastNotified: %v", got)
	}
}
