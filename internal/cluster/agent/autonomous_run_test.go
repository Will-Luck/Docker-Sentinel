package agent

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// cluster-agent-12: autonomous mode was torn down before every reconnect
// attempt and entered again after it fails (every 30 seconds at most), so
// it logged "entering autonomous mode" again and again and its poll never
// came round. It runs from the first failed attempt past the grace period
// until the agent connects again.
func TestAutonomous_RunsUntilTheAgentReconnects(t *testing.T) {
	var buf syncBuf
	a := newTestAgent(t.TempDir(), newFakeHost())
	a.log = slog.New(slog.NewTextHandler(&buf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for range 3 { // three failed reconnect attempts
		a.startAutonomous(ctx)
	}
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(buf.String(), "entering autonomous mode"); n != 1 {
		t.Errorf("entered autonomous mode %d times over three failed attempts, want once", n)
	}
	if !a.autonomousRunning() {
		t.Fatal("autonomous mode is not running")
	}
	a.setConnected()
	if a.autonomousRunning() {
		t.Error("autonomous mode still running once the agent connected")
	}
}
