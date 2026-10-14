package cluster

import "testing"

func TestRollbackTarget(t *testing.T) {
	upd := func(old string) HistoryRow { return HistoryRow{Outcome: "success", Type: "update", OldImage: old} }
	manual := HistoryRow{Outcome: "rollback", Error: RolledBackOnRequest}
	migrated := HistoryRow{Outcome: "success", Type: "rollback", OldImage: "ignored"}
	auto := HistoryRow{Outcome: "rollback", Error: "health check failed"}
	failed := HistoryRow{Outcome: "failed", OldImage: "nope"}

	tests := []struct {
		name string
		rows []HistoryRow
		want string
	}{
		{"no rows", nil, ""},
		{"newest update wins", []HistoryRow{upd("a:2"), upd("a:1")}, "a:2"},
		{"update without an old image is skipped", []HistoryRow{upd(""), upd("a:1")}, "a:1"},
		{"failed and automatic rollback rows are ignored", []HistoryRow{failed, auto, upd("a:1")}, "a:1"},
		{"manual rollback cancels the update before it", []HistoryRow{manual, upd("a:2")}, ""},
		{"manual rollback falls through to the next older update", []HistoryRow{manual, upd("a:2"), upd("a:1")}, "a:1"},
		{"two rollbacks cancel two updates", []HistoryRow{manual, manual, upd("a:3"), upd("a:2"), upd("a:1")}, "a:1"},
		{"update after a rollback is the target again", []HistoryRow{upd("a:3"), manual, upd("a:2")}, "a:3"},
		{"pre-v3 rollback row counts as a rollback", []HistoryRow{migrated, upd("a:2")}, ""},
		{"only rollbacks", []HistoryRow{manual, migrated}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RollbackTarget(tc.rows); got != tc.want {
				t.Errorf("RollbackTarget = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsManualRollback(t *testing.T) {
	tests := []struct {
		row  HistoryRow
		want bool
	}{
		{HistoryRow{Outcome: "rollback", Error: RolledBackOnRequest}, true},
		{HistoryRow{Outcome: "success", Type: "rollback"}, true},
		{HistoryRow{Outcome: "rollback", Error: "health check failed"}, false},
		{HistoryRow{Outcome: "success", Type: "update"}, false},
		{HistoryRow{Outcome: "failed", Type: "rollback"}, false},
	}
	for _, tc := range tests {
		if got := IsManualRollback(tc.row); got != tc.want {
			t.Errorf("IsManualRollback(%+v) = %v, want %v", tc.row, got, tc.want)
		}
	}
}

// RollbackTarget does not truncate: callers fetch RollbackHistoryDepth rows
// (see TestRemoteRollbackTargetFetchesTheHistoryDepth in the web package).
func TestRollbackHistoryDepth(t *testing.T) {
	if RollbackHistoryDepth != 50 {
		t.Fatalf("RollbackHistoryDepth = %d, want 50", RollbackHistoryDepth)
	}
	rows := make([]HistoryRow, 0, RollbackHistoryDepth+1)
	for i := 0; i < RollbackHistoryDepth; i++ {
		rows = append(rows, HistoryRow{Outcome: "failed"})
	}
	rows = append(rows, HistoryRow{Outcome: "success", OldImage: "a:1"})
	if got := RollbackTarget(rows); got != "a:1" {
		t.Errorf("target past the depth = %q, want a:1 (the function itself does not truncate)", got)
	}
}
