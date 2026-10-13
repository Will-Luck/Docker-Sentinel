package store

import "testing"

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
