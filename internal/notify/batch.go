package notify

import (
	"fmt"
	"strings"
)

// isBatchable returns true for event types that should be batched.
// Events like update_started, container_state, rollback_* pass through immediately
// since they represent unique, time-sensitive occurrences.
func isBatchable(t EventType) bool {
	switch t {
	case EventUpdateAvailable, EventVersionAvailable,
		EventUpdateSucceeded, EventUpdateFailed:
		return true
	default:
		return false
	}
}

// aggregateEvents groups batchable events by type and produces summary events.
// For update_available/version_available: "N updates available" with ContainerNames populated.
// For update_succeeded/update_failed: "N updates completed (X succeeded, Y failed)".
func aggregateEvents(events []Event) []Event {
	if len(events) == 0 {
		return nil
	}

	// Group by EventType.
	groups := make(map[EventType][]Event)
	for _, e := range events {
		groups[e.Type] = append(groups[e.Type], e)
	}

	var result []Event

	// Handle available events: each type gets its own summary.
	for _, t := range []EventType{EventUpdateAvailable, EventVersionAvailable} {
		evts, ok := groups[t]
		if !ok {
			continue
		}
		if len(evts) == 1 {
			e := evts[0]
			if len(e.ContainerNames) == 0 {
				e.ContainerNames = []string{e.ContainerName}
			}
			result = append(result, e)
			continue
		}
		names := make([]string, len(evts))
		for i, e := range evts {
			names[i] = e.ContainerName
		}
		result = append(result, Event{
			Type:           t,
			ContainerName:  fmt.Sprintf("%d containers", len(evts)),
			ContainerNames: names,
			Timestamp:      evts[len(evts)-1].Timestamp,
		})
	}

	// Handle succeeded/failed: combine into summary events.
	succeeded := groups[EventUpdateSucceeded]
	failed := groups[EventUpdateFailed]

	if len(succeeded) > 0 || len(failed) > 0 {
		total := len(succeeded) + len(failed)

		// If only one event total, pass through as-is.
		if total == 1 {
			var e Event
			if len(succeeded) == 1 {
				e = succeeded[0]
			} else {
				e = failed[0]
			}
			if len(e.ContainerNames) == 0 {
				e.ContainerNames = []string{e.ContainerName}
			}
			result = append(result, e)
		} else {
			// Multiple events: produce separate summaries for succeeded and failed.
			if len(succeeded) > 0 {
				names := make([]string, len(succeeded))
				for i, e := range succeeded {
					names[i] = e.ContainerName
				}
				summary := Event{
					Type:           EventUpdateSucceeded,
					ContainerName:  fmt.Sprintf("%d containers", len(succeeded)),
					ContainerNames: names,
					Timestamp:      succeeded[len(succeeded)-1].Timestamp,
				}
				if len(failed) > 0 {
					summary.Error = fmt.Sprintf("%d succeeded, %d failed", len(succeeded), len(failed))
				}
				result = append(result, summary)
			}
			if len(failed) > 0 {
				names := make([]string, len(failed))
				for i, e := range failed {
					names[i] = e.ContainerName
				}
				// Collect error messages from each failed container.
				var errs []string
				for _, e := range failed {
					if e.Error != "" {
						errs = append(errs, e.ContainerName+": "+e.Error)
					}
				}
				errMsg := strings.Join(errs, "; ")
				result = append(result, Event{
					Type:           EventUpdateFailed,
					ContainerName:  fmt.Sprintf("%d containers", len(failed)),
					ContainerNames: names,
					Error:          errMsg,
					Timestamp:      failed[len(failed)-1].Timestamp,
				})
			}
		}
	}

	return result
}
