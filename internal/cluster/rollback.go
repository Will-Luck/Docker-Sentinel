package cluster

// RollbackHistoryDepth is how many history rows the rollback rule reads.
const RollbackHistoryDepth = 50

// HistoryRow is what the rollback rule reads of an update history row.
type HistoryRow struct{ Outcome, Error, Type, OldImage string }

// IsManualRollback reports whether a row is a rollback asked for from the
// dashboard or the API (RollbackRow), or a remote rollback recorded before
// v3.0.0 (outcome "success", type "rollback").
func IsManualRollback(r HistoryRow) bool {
	return (r.Outcome == "rollback" && r.Error == RolledBackOnRequest) ||
		(r.Outcome == "success" && r.Type == "rollback")
}

// RollbackTarget is the image a rollback goes back to, from rows newest
// first: the old image of the newest successful update that no later manual
// rollback cancelled, or "".
func RollbackTarget(rows []HistoryRow) string {
	pending := 0
	for _, r := range rows {
		switch {
		case IsManualRollback(r):
			pending++
		case r.Outcome == "success" && r.OldImage != "":
			if pending > 0 {
				pending--
				continue
			}
			return r.OldImage
		}
	}
	return ""
}
