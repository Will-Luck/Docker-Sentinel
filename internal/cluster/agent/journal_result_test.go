package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// brokenStream fails every send, as a Channel whose connection dropped.
type brokenStream struct{ recStream }

func (*brokenStream) Send(*proto.AgentMessage) error { return errors.New("transport is closing") }

// live-15, cluster-agent-8: an update that finished while the connection
// to the hub was down was lost: its result could not be sent, and the
// offline journal meant to carry it was never written. The result goes to
// the journal, which the agent sends when it reconnects.
func TestHandleUpdateContainer_UnsentResultGoesToTheJournal(t *testing.T) {
	h, _, _ := recreateFixture(t)
	a := newSwapAgent(t, h)
	j, err := newJournal(a.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	a.journal = j

	req := &proto.UpdateContainerRequest{ContainerName: "web", TargetImage: "nginx:2"}
	_ = a.handleUpdateContainer(context.Background(), &brokenStream{}, req, "req-9")

	entries := a.journal.Entries()
	if len(entries) != 1 {
		t.Fatalf("journal = %+v, want the update's result", entries)
	}
	e := entries[0]
	if e.ID != "req-9" || e.Action != "update" || e.Container != "web" || e.Outcome != "success" || e.NewImage != "nginx:2" || e.OldImage != "nginx:1" {
		t.Errorf("journal entry = %+v, want the successful update of web to nginx:2", e)
	}
	h.requireOnly(t, "web", "nginx:2", true)
}
