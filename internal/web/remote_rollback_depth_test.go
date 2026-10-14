package web

import (
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
)

// depthRecordingHistory records the row limit a history read asks for.
type depthRecordingHistory struct {
	nopHistory
	key   string
	limit int
}

func (h *depthRecordingHistory) ListHistoryByContainer(key string, limit int) ([]UpdateRecord, error) {
	h.key, h.limit = key, limit
	return nil, nil
}

// The dashboard reads as much history as the rollback itself does, so the
// button and the action agree.
func TestRemoteRollbackTargetFetchesTheHistoryDepth(t *testing.T) {
	h := &depthRecordingHistory{}
	srv := &Server{deps: Dependencies{Store: h}}
	srv.remoteRollbackTarget("h1", "web")
	if h.limit != cluster.RollbackHistoryDepth {
		t.Errorf("history limit = %d, want %d", h.limit, cluster.RollbackHistoryDepth)
	}
	if h.key != "h1::web" {
		t.Errorf("history key = %q, want h1::web", h.key)
	}
}
