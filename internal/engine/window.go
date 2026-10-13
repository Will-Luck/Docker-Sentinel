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

const minutesPerWeek = 7 * 24 * 60

// weekRange returns a weekly window's start and end as minutes of the week
// (Sunday 00:00 is 0). Without an end day, or with the start day again, an
// end at or before the start falls on the next day.
func (s windowSpec) weekRange() (start, end int) {
	start = s.weekday*1440 + s.startHour*60 + s.startMin
	endMins := s.endHour*60 + s.endMin
	if s.endWeekday >= 0 && s.endWeekday != s.weekday {
		return start, s.endWeekday*1440 + endMins
	}
	end = s.weekday*1440 + endMins
	if endMins <= s.startHour*60+s.startMin {
		end += 1440
	}
	return start, end % minutesPerWeek
}

func (s windowSpec) matches(t time.Time) bool {
	nowMins := t.Hour()*60 + t.Minute()

	// Daily window (no weekday constraint).
	if s.weekday < 0 {
		startMins := s.startHour*60 + s.startMin
		endMins := s.endHour*60 + s.endMin
		if startMins <= endMins {
			return nowMins >= startMins && nowMins < endMins
		}
		return nowMins >= startMins || nowMins < endMins
	}

	// Weekly window: test on the week's circle of minutes, so a window
	// over several days is open on the days between, and one that runs
	// past the end of the week wraps.
	now := int(t.Weekday())*1440 + nowMins
	start, end := s.weekRange()
	if start <= end {
		return now >= start && now < end
	}
	return now >= start || now < end
}

// NextOpen returns the earliest time from t, to the minute, at which the
// window is open: t itself when it is open now. A nil window is always open.
// It looks a week ahead; a window that never opens gives t plus a week.
func (w *MaintenanceWindow) NextOpen(t time.Time) time.Time {
	t = t.Truncate(time.Minute)
	if w.IsOpen(t) {
		return t
	}
	best := t.Add(7 * 24 * time.Hour)
	for _, s := range w.windows {
		for day := range 8 {
			d := t.AddDate(0, 0, day)
			if s.weekday >= 0 && int(d.Weekday()) != s.weekday {
				continue
			}
			open := time.Date(d.Year(), d.Month(), d.Day(), s.startHour, s.startMin, 0, 0, t.Location())
			if open.After(t) && open.Before(best) {
				best = open
				break
			}
		}
	}
	return best
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
