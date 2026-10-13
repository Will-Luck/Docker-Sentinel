package engine

import (
	"testing"
	"time"
)

// With an interval under a day, the next digest after the last slot of the
// day is the next morning's, not one a minute later (the delay went
// negative and was clamped to a minute, so the digest fired every minute
// until midnight).
func TestDigestNextSlotIsAlwaysAhead(t *testing.T) {
	for _, tc := range []struct {
		now, interval string
		want          time.Duration
	}{
		{"21:00", "12h", 12 * time.Hour}, // just fired the evening slot
		{"21:30", "12h", 11*time.Hour + 30*time.Minute},
		{"08:00", "12h", time.Hour},    // before the first slot
		{"10:00", "6h", 5 * time.Hour}, // 09:00, 15:00, 21:00, 03:00
		{"23:00", "6h", 4 * time.Hour},
		{"09:00", "24h", 24 * time.Hour},
		{"09:00", "48h", 48 * time.Hour},   // just fired: two days on
		{"09:00", "168h", 168 * time.Hour}, // weekly: the same weekday next week
		{"10:00", "168h", 167 * time.Hour},
		{"08:00", "48h", time.Hour},
	} {
		now, _ := time.Parse("15:04", tc.now)
		clk := newMockClock(time.Date(2026, 3, 10, now.Hour(), now.Minute(), 0, 0, time.UTC))
		d := NewDigestScheduler(nil, nil, nil, nil, nil, clk)
		d.SetSettingsReader(&testSettings{data: map[string]string{"digest_time": "09:00", "digest_interval": tc.interval}})

		if got := d.timeUntilNext(); got != tc.want {
			t.Errorf("at %s every %s: next in %v, want %v", tc.now, tc.interval, got, tc.want)
		}
	}
}
