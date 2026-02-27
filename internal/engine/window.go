package engine

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaintenanceWindow represents one or more time windows when auto-updates are allowed.
type MaintenanceWindow struct {
	windows []windowSpec
}

type windowSpec struct {
	// Daily window: startHour, startMin, endHour, endMin
	startHour, startMin int
	endHour, endMin     int
	// Weekly: if >= 0, specific weekday (0=Sunday, 6=Saturday). -1 means daily.
	weekday    int
	endWeekday int // -1 if same as weekday or unset; >= 0 for cross-day windows
}

// ParseWindow parses a maintenance window expression.
// Formats:
//   - "HH:MM-HH:MM" — daily window (supports midnight crossing, e.g. "23:00-05:00")
//   - "Mon HH:MM-Mon HH:MM" — weekly window (e.g. "Sat 02:00-Sat 06:00")
//   - Multiple windows separated by ";" (e.g. "02:00-06:00;Sat 00:00-Sun 00:00")
//   - Empty string returns nil (no window = always open)
//
// Returns error for malformed expressions. Callers should fail-open on error.
func ParseWindow(expr string) (*MaintenanceWindow, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil
	}

	parts := strings.Split(expr, ";")
	var specs []windowSpec
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		spec, err := parseOneWindow(p)
		if err != nil {
			return nil, fmt.Errorf("invalid window %q: %w", p, err)
		}
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		return nil, nil
	}
	return &MaintenanceWindow{windows: specs}, nil
}

// IsOpen returns true if the given time falls within any maintenance window.
// A nil MaintenanceWindow is always open (no restriction).
func (w *MaintenanceWindow) IsOpen(t time.Time) bool {
	if w == nil || len(w.windows) == 0 {
		return true
	}
	for _, s := range w.windows {
		if s.matches(t) {
			return true
		}
	}
	return false
}

func (s windowSpec) matches(t time.Time) bool {
	nowMins := t.Hour()*60 + t.Minute()
	startMins := s.startHour*60 + s.startMin
	endMins := s.endHour*60 + s.endMin
	wd := int(t.Weekday())

	// Daily window (no weekday constraint).
	if s.weekday < 0 {
		if startMins <= endMins {
			return nowMins >= startMins && nowMins < endMins
		}
		return nowMins >= startMins || nowMins < endMins
	}

	// Weekly window that spans into a different weekday (e.g. "Sat 22:00-Sun 06:00").
	if s.endWeekday >= 0 && s.endWeekday != s.weekday {
		if wd == s.weekday {
			// On the start day: must be at or after start time.
			return nowMins >= startMins
		}
		if wd == s.endWeekday {
			// On the end day: must be before end time.
			return nowMins < endMins
		}
		return false
	}

	// Weekly window within a single weekday (e.g. "Sat 02:00-Sat 06:00").
	if wd != s.weekday {
		return false
	}
	if startMins <= endMins {
		return nowMins >= startMins && nowMins < endMins
	}
	return nowMins >= startMins || nowMins < endMins
}

var weekdayNames = map[string]int{
	"sun": 0, "sunday": 0,
	"mon": 1, "monday": 1,
	"tue": 2, "tuesday": 2,
	"wed": 3, "wednesday": 3,
	"thu": 4, "thursday": 4,
	"fri": 5, "friday": 5,
	"sat": 6, "saturday": 6,
}

func parseOneWindow(expr string) (windowSpec, error) {
	// Try weekly format first: "Day HH:MM-Day HH:MM" or "Day HH:MM-HH:MM"
	parts := strings.SplitN(expr, "-", 2)
	if len(parts) != 2 {
		return windowSpec{}, fmt.Errorf("expected HH:MM-HH:MM format")
	}

	startPart := strings.TrimSpace(parts[0])
	endPart := strings.TrimSpace(parts[1])

	// Check for weekday prefix on start
	startDay := -1
	startFields := strings.Fields(startPart)
	if len(startFields) == 2 {
		d, ok := weekdayNames[strings.ToLower(startFields[0])]
		if !ok {
			return windowSpec{}, fmt.Errorf("unknown weekday %q", startFields[0])
		}
		startDay = d
		startPart = startFields[1]
	}

	// Check for weekday prefix on end.
	endDay := -1
	endFields := strings.Fields(endPart)
	if len(endFields) == 2 {
		d, ok := weekdayNames[strings.ToLower(endFields[0])]
		if !ok {
			return windowSpec{}, fmt.Errorf("unknown weekday %q", endFields[0])
		}
		endDay = d
		endPart = endFields[1]
	}

	sh, sm, err := parseTime(startPart)
	if err != nil {
		return windowSpec{}, fmt.Errorf("start time: %w", err)
	}
	eh, em, err := parseTime(endPart)
	if err != nil {
		return windowSpec{}, fmt.Errorf("end time: %w", err)
	}

	return windowSpec{
		startHour: sh, startMin: sm,
		endHour: eh, endMin: em,
		weekday:    startDay,
		endWeekday: endDay,
	}, nil
}

func parseTime(s string) (hour, min int, err error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected HH:MM, got %q", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, fmt.Errorf("invalid hour %q", parts[0])
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("invalid minute %q", parts[1])
	}
	return h, m, nil
}
