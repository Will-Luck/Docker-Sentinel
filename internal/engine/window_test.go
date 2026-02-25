package engine

import (
	"testing"
	"time"
)

func TestParseWindow_Empty(t *testing.T) {
	w, err := ParseWindow("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != nil {
		t.Fatal("expected nil window for empty string")
	}
	// Nil window is always open.
	if !w.IsOpen(time.Now()) {
		t.Fatal("nil window should always be open")
	}
}

func TestParseWindow_WhitespaceOnly(t *testing.T) {
	w, err := ParseWindow("   ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != nil {
		t.Fatal("expected nil window for whitespace-only string")
	}
}

func TestParseWindow_DailyWindow(t *testing.T) {
	w, err := ParseWindow("02:00-06:00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w == nil {
		t.Fatal("expected non-nil window")
	}

	tests := []struct {
		name   string
		time   time.Time
		expect bool
	}{
		{"inside at 03:00", makeTime(2025, 1, 1, 3, 0), true},
		{"inside at 02:00 (start inclusive)", makeTime(2025, 1, 1, 2, 0), true},
		{"inside at 05:59", makeTime(2025, 1, 1, 5, 59), true},
		{"outside at 06:00 (end exclusive)", makeTime(2025, 1, 1, 6, 0), false},
		{"outside at 01:59", makeTime(2025, 1, 1, 1, 59), false},
		{"outside at 12:00", makeTime(2025, 1, 1, 12, 0), false},
		{"outside at 23:00", makeTime(2025, 1, 1, 23, 0), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := w.IsOpen(tt.time)
			if got != tt.expect {
				t.Errorf("IsOpen(%v) = %v, want %v", tt.time, got, tt.expect)
			}
		})
	}
}

func TestParseWindow_MidnightCrossing(t *testing.T) {
	w, err := ParseWindow("23:00-05:00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tests := []struct {
		name   string
		time   time.Time
		expect bool
	}{
		{"inside before midnight at 23:30", makeTime(2025, 1, 1, 23, 30), true},
		{"inside at 23:00 (start inclusive)", makeTime(2025, 1, 1, 23, 0), true},
		{"inside after midnight at 02:00", makeTime(2025, 1, 2, 2, 0), true},
		{"inside at 04:59", makeTime(2025, 1, 2, 4, 59), true},
		{"outside at 05:00 (end exclusive)", makeTime(2025, 1, 2, 5, 0), false},
		{"outside at 12:00", makeTime(2025, 1, 1, 12, 0), false},
		{"outside at 22:59", makeTime(2025, 1, 1, 22, 59), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := w.IsOpen(tt.time)
			if got != tt.expect {
				t.Errorf("IsOpen(%v) = %v, want %v", tt.time, got, tt.expect)
			}
		})
	}
}

func TestParseWindow_WeeklyWindow(t *testing.T) {
	// Saturday = weekday 6
	w, err := ParseWindow("Sat 02:00-Sat 06:00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 2025-01-04 is a Saturday
	tests := []struct {
		name   string
		time   time.Time
		expect bool
	}{
		{"right day inside at 03:00", makeTime(2025, 1, 4, 3, 0), true},
		{"right day at start 02:00", makeTime(2025, 1, 4, 2, 0), true},
		{"right day at end 06:00 (exclusive)", makeTime(2025, 1, 4, 6, 0), false},
		{"right day outside at 12:00", makeTime(2025, 1, 4, 12, 0), false},
		{"wrong day (Sunday) inside time", makeTime(2025, 1, 5, 3, 0), false},
		{"wrong day (Friday) inside time", makeTime(2025, 1, 3, 3, 0), false},
		{"wrong day (Monday) at 02:30", makeTime(2025, 1, 6, 2, 30), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := w.IsOpen(tt.time)
			if got != tt.expect {
				t.Errorf("IsOpen(%v) = %v, want %v (weekday=%v)", tt.time, got, tt.expect, tt.time.Weekday())
			}
		})
	}
}

func TestParseWindow_WeeklyShortNames(t *testing.T) {
	// Test various weekday name formats.
	cases := []struct {
		expr    string
		weekday time.Weekday
	}{
		{"Sun 01:00-Sun 02:00", time.Sunday},
		{"Monday 01:00-Monday 02:00", time.Monday},
		{"tue 01:00-tue 02:00", time.Tuesday},
		{"Wed 01:00-Wed 02:00", time.Wednesday},
		{"thu 01:00-thu 02:00", time.Thursday},
		{"Friday 01:00-Friday 02:00", time.Friday},
		{"sat 01:00-sat 02:00", time.Saturday},
	}

	for _, c := range cases {
		w, err := ParseWindow(c.expr)
		if err != nil {
			t.Errorf("ParseWindow(%q): unexpected error: %v", c.expr, err)
			continue
		}
		if w == nil {
			t.Errorf("ParseWindow(%q): unexpected nil", c.expr)
			continue
		}
		// Find a date matching the expected weekday.
		// 2025-01-05 is Sunday, so offset from there.
		base := time.Date(2025, 1, 5, 1, 30, 0, 0, time.UTC) // Sunday
		target := base.AddDate(0, 0, int(c.weekday)-int(time.Sunday))
		if !w.IsOpen(target) {
			t.Errorf("ParseWindow(%q): expected open on %v (%v)", c.expr, target, target.Weekday())
		}
	}
}

func TestParseWindow_MultipleWindows(t *testing.T) {
	w, err := ParseWindow("02:00-06:00;22:00-23:00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tests := []struct {
		name   string
		time   time.Time
		expect bool
	}{
		{"first window match at 03:00", makeTime(2025, 1, 1, 3, 0), true},
		{"second window match at 22:30", makeTime(2025, 1, 1, 22, 30), true},
		{"neither window at 12:00", makeTime(2025, 1, 1, 12, 0), false},
		{"between windows at 07:00", makeTime(2025, 1, 1, 7, 0), false},
		{"between windows at 21:59", makeTime(2025, 1, 1, 21, 59), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := w.IsOpen(tt.time)
			if got != tt.expect {
				t.Errorf("IsOpen(%v) = %v, want %v", tt.time, got, tt.expect)
			}
		})
	}
}

func TestParseWindow_InvalidExpressions(t *testing.T) {
	cases := []string{
		"not-a-time",
		"25:00-06:00",         // invalid hour
		"02:00-06:60",         // invalid minute
		"02:00",               // missing end
		"Xyz 02:00-Xyz 06:00", // unknown weekday
		"02:00-Xyz 06:00",     // unknown weekday on end only
		"aa:00-06:00",         // non-numeric hour
		"02:bb-06:00",         // non-numeric minute
	}

	for _, c := range cases {
		_, err := ParseWindow(c)
		if err == nil {
			t.Errorf("ParseWindow(%q): expected error, got nil", c)
		}
	}
}

func TestParseWindow_BoundaryStartInclusive_EndExclusive(t *testing.T) {
	w, err := ParseWindow("10:00-10:30")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tests := []struct {
		name   string
		time   time.Time
		expect bool
	}{
		{"at start (inclusive)", makeTime(2025, 1, 1, 10, 0), true},
		{"inside", makeTime(2025, 1, 1, 10, 15), true},
		{"at end (exclusive)", makeTime(2025, 1, 1, 10, 30), false},
		{"just before start", makeTime(2025, 1, 1, 9, 59), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := w.IsOpen(tt.time)
			if got != tt.expect {
				t.Errorf("IsOpen(%v) = %v, want %v", tt.time, got, tt.expect)
			}
		})
	}
}

func TestParseWindow_SemicolonWithEmptyParts(t *testing.T) {
	// Extra semicolons should be ignored.
	w, err := ParseWindow(";02:00-06:00;;")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w == nil {
		t.Fatal("expected non-nil window")
	}
	if !w.IsOpen(makeTime(2025, 1, 1, 3, 0)) {
		t.Error("expected open at 03:00")
	}
}

func TestParseWindow_NilIsAlwaysOpen(t *testing.T) {
	var w *MaintenanceWindow
	if !w.IsOpen(time.Now()) {
		t.Error("nil MaintenanceWindow should always be open")
	}
}

func TestParseWindow_EmptyWindowsAlwaysOpen(t *testing.T) {
	w := &MaintenanceWindow{} // zero windows
	if !w.IsOpen(time.Now()) {
		t.Error("empty MaintenanceWindow should always be open")
	}
}

func makeTime(year, month, day, hour, min int) time.Time {
	return time.Date(year, time.Month(month), day, hour, min, 0, 0, time.UTC)
}
