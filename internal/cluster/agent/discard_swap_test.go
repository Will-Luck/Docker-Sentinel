package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

func discardAction(t *testing.T, a *Agent, name string) *proto.ContainerActionResult {
	t.Helper()
	st := &recStream{}
	req := &proto.ContainerActionRequest{ContainerName: name, Action: actionDiscardSwap}
	if err := a.handleContainerAction(context.Background(), st, req, "req-1"); err != nil {
		t.Fatal(err)
	}
	for _, m := range st.messages() {
		if r := m.GetContainerActionResult(); r != nil {
			return r
		}
	}
	t.Fatal("no action result sent")
	return nil
}

// A swap the agent cannot finish on its own is discarded from the hub
// (DELETE /api/swaps/{name}?host=...), once the containers are right on the
// host: the agent removes its journal record, and says when there is none.
func TestHandleContainerAction_DiscardsASwapRecord(t *testing.T) {
	h := newFakeHost()
	s := openTestStore(t)
	if err := s.SaveSwap(store.SwapRecord{Name: "web", Kind: "update", OldID: "gone"}); err != nil {
		t.Fatal(err)
	}
	a := newSwapAgentOn(t, h, s, clock.Real{})

	if r := discardAction(t, a, "web"); r.GetOutcome() != "success" {
		t.Fatalf("discard: %q (%s), want success", r.GetOutcome(), r.GetError())
	}
	if s.HasSwap("web") {
		t.Error("the swap record is still there")
	}
	if r := discardAction(t, a, "web"); r.GetOutcome() != discardNotFound {
		t.Errorf("discard with no record: %q (%s), want %s", r.GetOutcome(), r.GetError(), discardNotFound)
	}
}

// An agent refusing an update for an unresolved swap says how to discard
// the record from the hub.
func TestRecreate_PendingSwapNamesTheHubEndpoint(t *testing.T) {
	h, _, _ := recreateFixture(t)
	s := openTestStore(t)
	if err := s.SaveSwap(store.SwapRecord{Name: "web", Kind: "update", OldID: "gone"}); err != nil {
		t.Fatal(err)
	}
	a := newSwapAgentOn(t, h, s, clock.Real{})
	a.hostID = "h1"
	_, err := a.recreateContainer(context.Background(), "web", "nginx:2")
	if err == nil || !strings.Contains(err.Error(), "DELETE /api/swaps/web?host=h1") {
		t.Errorf("error = %v, want it to name DELETE /api/swaps/web?host=h1", err)
	}
}
