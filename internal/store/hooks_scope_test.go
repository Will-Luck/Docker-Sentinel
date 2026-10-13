package store

import (
	"testing"
	"time"
)

// Remote containers' hooks are stored under "hostID::name". A local
// container whose name is a host ID must not get every hook of that
// host's containers.
func TestListHooks_OnlyTheContainersOwn(t *testing.T) {
	s := testStore(t)
	for _, h := range []HookEntry{
		{ContainerName: "h1", Phase: "pre-update", Command: []string{"echo", "local"}},
		{ContainerName: "h1::web", Phase: "pre-update", Command: []string{"echo", "remote"}},
		{ContainerName: "h1::web", Phase: "post-update", Command: []string{"echo", "remote"}},
	} {
		if err := s.SaveHook(h); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListHooks("h1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ContainerName != "h1" {
		t.Errorf("ListHooks(h1) = %+v, want only h1's own hook", got)
	}
	remote, err := s.ListHooks("h1::web")
	if err != nil {
		t.Fatal(err)
	}
	if len(remote) != 2 {
		t.Errorf("ListHooks(h1::web) = %+v, want its two hooks", remote)
	}
}

// The failure row of a remote update the agent did not answer is written
// only if no row exists at its time: the agent's result, recorded at the
// same time, may have got there first.
func TestRecordUpdateIfAbsent(t *testing.T) {
	s := testStore(t)
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := s.RecordUpdate(UpdateRecord{Timestamp: at, ContainerName: "h1::web", Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	ok, err := s.RecordUpdateIfAbsent(UpdateRecord{Timestamp: at, ContainerName: "h1::web", Outcome: "failed"})
	if err != nil || ok {
		t.Fatalf("RecordUpdateIfAbsent over an existing row = %v, %v; want not written", ok, err)
	}
	ok, err = s.RecordUpdateIfAbsent(UpdateRecord{Timestamp: at.Add(time.Second), ContainerName: "h1::web", Outcome: "failed"})
	if err != nil || !ok {
		t.Fatalf("RecordUpdateIfAbsent at a free time = %v, %v; want written", ok, err)
	}
	recs, _ := s.ListHistoryByContainer("h1::web", 10)
	if len(recs) != 2 || recs[1].Outcome != "success" {
		t.Errorf("history = %+v, want the success kept and the later failure added", recs)
	}
}
