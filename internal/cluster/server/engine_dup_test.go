package server

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// testServerWithLog is testServer with the server's log captured.
func testServerWithLog(t *testing.T) (*Server, string, *syncBuffer) {
	t.Helper()
	ca, err := cluster.EnsureCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir() + "/hub.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logs := &syncBuffer{}
	srv, err := New(ca, st, events.New(), slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	lis.Close()
	if err := srv.Start(addr); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	return srv, addr, logs
}

// The same Docker engine enrolled twice shows up as two hosts with the
// same containers, and approvals for both race on one daemon. The hub must
// say so (it does not guess which one to remove).
func TestReportState_WarnsWhenAnEngineIsEnrolledTwice(t *testing.T) {
	srv, addr, logs := testServerWithLog(t)
	report := func() string {
		token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
		hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
		client := proto.NewAgentServiceClient(agentTLSConn(t, addr, certPEM, keyPEM, caPEM))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := client.ReportState(ctx, &proto.StateReport{HostId: hostID, EngineId: "ENGINE-1"}); err != nil {
			t.Fatalf("ReportState: %v", err)
		}
		return hostID
	}
	first := report()
	second := report()

	out := logs.String()
	if !strings.Contains(out, "same Docker engine") || !strings.Contains(out, first) || !strings.Contains(out, second) {
		t.Errorf("hub log does not warn that %s and %s are the same engine:\n%s", first, second, out)
	}
	hs, _ := srv.Registry().Get(second)
	if len(hs.EngineSharedWith) != 1 || hs.EngineSharedWith[0] != first {
		t.Errorf("EngineSharedWith = %v, want [%s]", hs.EngineSharedWith, first)
	}
	if hs, _ := srv.Registry().Get(first); len(hs.EngineSharedWith) != 1 || hs.EngineSharedWith[0] != second {
		t.Errorf("first host EngineSharedWith = %v, want [%s]", hs.EngineSharedWith, second)
	}
}
