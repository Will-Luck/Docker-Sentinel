package engine

import (
	"testing"
	"time"
)

// at returns the time on the given weekday of the week of 2026-03-08 (a
// Sunday) at hh:mm.
func at(weekday time.Weekday, hh, mm int) time.Time {
	return time.Date(2026, 3, 8+int(weekday), hh, mm, 0, 0, time.UTC)
}

// A weekly window that spans several days is open on the days between, and
// a same-day form whose end is before its start runs into the next day.
func TestWeeklyWindowsAcrossDays(t *testing.T) {
	for _, tc := range []struct {
		expr string
		t    time.Time
		open bool
	}{
		{"Fri 22:00-Mon 06:00", at(time.Saturday, 12, 0), true},
		{"Fri 22:00-Mon 06:00", at(time.Sunday, 3, 0), true},
		{"Fri 22:00-Mon 06:00", at(time.Monday, 5, 59), true},
		{"Fri 22:00-Mon 06:00", at(time.Monday, 6, 0), false},
		{"Fri 22:00-Mon 06:00", at(time.Friday, 21, 59), false},
		{"Sat 22:00-02:00", at(time.Sunday, 1, 0), true},
		{"Sat 22:00-02:00", at(time.Saturday, 1, 0), false},
		{"Sat 22:00-02:00", at(time.Saturday, 23, 0), true},
		{"Sat 22:00-Sun 06:00", at(time.Sunday, 5, 0), true},
		{"Sat 22:00-Sun 06:00", at(time.Sunday, 7, 0), false},
		{"Sat 02:00-Sat 06:00", at(time.Saturday, 3, 0), true},
		{"Sat 00:00-Sun 00:00", at(time.Saturday, 23, 59), true},
		{"Sat 00:00-Sun 00:00", at(time.Sunday, 0, 0), false},
		{"23:00-05:00", at(time.Wednesday, 4, 0), true},
	} {
		w, err := ParseWindow(tc.expr)
		if err != nil {
			t.Fatalf("%s: %v", tc.expr, err)
		}
		if got := w.IsOpen(tc.t); got != tc.open {
			t.Errorf("%s at %s: open = %v, want %v", tc.expr, tc.t.Format("Mon 15:04"), got, tc.open)
		}
	}
}

// NextOpen gives the time a closed window opens next, so the scheduler can
// wake for it.
func TestWindowNextOpen(t *testing.T) {
	for _, tc := range []struct {
		expr      string
		now, want time.Time
	}{
		{"02:00-04:00", at(time.Monday, 10, 0), at(time.Tuesday, 2, 0)},
		{"02:00-04:00", at(time.Monday, 1, 30), at(time.Monday, 2, 0)},
		{"02:00-04:00", at(time.Monday, 3, 0), at(time.Monday, 3, 0)}, // open now
		{"Sat 02:00-Sat 06:00;Wed 01:00-02:00", at(time.Monday, 10, 0), at(time.Wednesday, 1, 0)},
		{"Fri 22:00-Mon 06:00", at(time.Monday, 7, 0), at(time.Friday, 22, 0)},
	} {
		w, _ := ParseWindow(tc.expr)
		if got := w.NextOpen(tc.now); !got.Equal(tc.want) {
			t.Errorf("%s from %s: next open %s, want %s", tc.expr, tc.now.Format("Mon 15:04"), got.Format("Mon 15:04"), tc.want.Format("Mon 15:04"))
		}
	}
}

// With a maintenance window set and closed, the scheduler wakes when it
// opens rather than waiting out the poll interval, which with the 6h
// default could miss a short window altogether.
func TestSchedulerWakesWhenWindowOpens(t *testing.T) {
	for _, tc := range []struct {
		window string
		want   time.Duration
	}{
		{"02:00-04:00", 3 * time.Hour}, // closed at 23:00: opens in 3h
		{"", 6 * time.Hour},
		{"22:00-23:30", 6 * time.Hour}, // open now: the poll interval as usual
	} {
		mock := newMockDocker()
		u, clk := newTestUpdater(t, mock)
		clk.Advance(23 * time.Hour) // 23:00
		u.SetSettingsReader(&testSettings{data: map[string]string{"maintenance_window": tc.window}})
		u.cfg.SetPollInterval(6 * time.Hour)
		s := NewScheduler(u, u.cfg, u.log, clk)

		if got := (<-s.nextTick()).Sub(clk.Now()); got != tc.want {
			t.Errorf("window %q: next tick in %v, want %v", tc.window, got, tc.want)
		}
	}
}
