package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// hookHost has web on nginx:1 (with a hook label for each phase) and
// nginx:2 in the registry, and an agent with hooks on.
func hookHost(t *testing.T) (*fakeHost, *Agent) {
	t.Helper()
	h := newFakeHost()
	c := h.addContainer("web", "nginx:1", true)
	c.labels = map[string]string{
		"sentinel.hook.pre-update":  "label-pre",
		"sentinel.hook.post-update": "label-post",
	}
	h.registry["nginx:2"] = "sha256:nginx-2"
	a := newSwapAgent(t, h)
	a.settings.SetHooksEnabled(true)
	return h, a
}

// updateWith sends the agent an update of web to nginx:2 built by edit.
func updateWith(t *testing.T, a *Agent, edit func(*proto.UpdateContainerRequest)) *proto.UpdateResult {
	t.Helper()
	req := &proto.UpdateContainerRequest{ContainerName: "web", TargetImage: "nginx:2"}
	if edit != nil {
		edit(req)
	}
	st := &recStream{}
	if err := a.handleUpdateContainer(context.Background(), st, req, "req-1"); err != nil {
		t.Fatal(err)
	}
	for _, m := range st.messages() {
		if ur := m.GetUpdateResult(); ur != nil {
			return ur
		}
	}
	t.Fatal("agent sent no UpdateResult")
	return nil
}

func hubHooks(pre, post string) func(*proto.UpdateContainerRequest) {
	return func(req *proto.UpdateContainerRequest) {
		req.Hooks = []*proto.HookSpec{
			{Phase: "pre-update", Command: []string{"hub-" + pre}},
			{Phase: "post-update", Command: []string{"hub-" + post}},
		}
	}
}

// The container's hook labels run around a remote update: the pre-update
// hook in the old container before anything changes, the post-update hook
// in the new one.
func TestRemoteUpdate_RunsLabelHooks(t *testing.T) {
	h, a := hookHost(t)
	if ur := updateWith(t, a, nil); ur.GetOutcome() != "success" {
		t.Fatalf("outcome %q (%s), want success", ur.GetOutcome(), ur.GetError())
	}
	want := []string{"web@nginx:1: /bin/sh -c label-pre", "web@nginx:2: /bin/sh -c label-post"}
	if !slices.Equal(h.execs, want) {
		t.Errorf("hooks run = %q, want %q", h.execs, want)
	}
}

// Hooks the hub stores for the container win over its labels, as on the
// hub.
func TestRemoteUpdate_HubHooksWinOverLabels(t *testing.T) {
	h, a := hookHost(t)
	if ur := updateWith(t, a, hubHooks("pre", "post")); ur.GetOutcome() != "success" {
		t.Fatalf("outcome %q (%s), want success", ur.GetOutcome(), ur.GetError())
	}
	want := []string{"web@nginx:1: hub-pre", "web@nginx:2: hub-post"}
	if !slices.Equal(h.execs, want) {
		t.Errorf("hooks run = %q, want %q", h.execs, want)
	}
}

// A pre-update hook that exits 75 skips the update: nothing is pulled or
// changed, and the hub hears it was skipped, not failed.
func TestRemoteUpdate_PreHookExit75Skips(t *testing.T) {
	h, a := hookHost(t)
	a.hubSkips.Store(true)
	h.execExit["hub-pre"] = 75
	ur := updateWith(t, a, hubHooks("pre", "post"))
	if ur.GetOutcome() != "skipped" {
		t.Fatalf("outcome %q (%s), want skipped", ur.GetOutcome(), ur.GetError())
	}
	if len(h.pullCalls) != 0 {
		t.Errorf("pulled %v although the hook asked to skip", h.pullCalls)
	}
	h.requireOnly(t, "web", "nginx:1", true)
}

// Any other pre-update hook failure aborts the update before anything
// changes.
func TestRemoteUpdate_PreHookFailureAborts(t *testing.T) {
	h, a := hookHost(t)
	h.execExit["hub-pre"] = 1
	ur := updateWith(t, a, hubHooks("pre", "post"))
	if ur.GetOutcome() != "failed" || !strings.Contains(ur.GetError(), "pre-update hook") {
		t.Fatalf("outcome %q (%s), want failed naming the pre-update hook", ur.GetOutcome(), ur.GetError())
	}
	if len(h.pullCalls) != 0 {
		t.Errorf("pulled %v although the hook failed", h.pullCalls)
	}
	h.requireOnly(t, "web", "nginx:1", true)
}

// A failing post-update hook does not undo the update; the hub is told.
func TestRemoteUpdate_PostHookFailureIsReported(t *testing.T) {
	h, a := hookHost(t)
	h.execExit["hub-post"] = 3
	ur := updateWith(t, a, hubHooks("pre", "post"))
	if ur.GetOutcome() != "success" || !strings.Contains(ur.GetError(), "post-update hook") {
		t.Fatalf("outcome %q (%s), want success with a warning about the post-update hook", ur.GetOutcome(), ur.GetError())
	}
	h.requireOnly(t, "web", "nginx:2", true)
}

// With hooks off, or when the hub asks to skip them, none run.
func TestRemoteUpdate_NoHooksWhenOffOrSkipped(t *testing.T) {
	for _, tc := range []struct {
		name string
		off  bool
		skip bool
	}{{"hooks off", true, false}, {"skip_hooks", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			h, a := hookHost(t)
			a.settings.SetHooksEnabled(!tc.off)
			ur := updateWith(t, a, func(req *proto.UpdateContainerRequest) {
				hubHooks("pre", "post")(req)
				req.SkipHooks = tc.skip
			})
			if ur.GetOutcome() != "success" {
				t.Fatalf("outcome %q (%s), want success", ur.GetOutcome(), ur.GetError())
			}
			if len(h.execs) != 0 {
				t.Errorf("hooks run = %s, want none", fmt.Sprint(h.execs))
			}
		})
	}
}

// review (minor): a hub before v3.0.0 counts any outcome but "success" as a
// failed update, so a pre-update hook's deliberate skip (exit 75) showed as
// a failure there. Such a hub (it does not advertise "update-skipped") is
// told "success", with the skip in the error text; a v3 hub is told
// "skipped".
func TestRemoteUpdate_SkipReportedAsTheHubUnderstands(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hubSkips bool
		want     string
	}{{"v3 hub", true, "skipped"}, {"older hub", false, "success"}} {
		t.Run(tc.name, func(t *testing.T) {
			h, a := hookHost(t)
			a.hubSkips.Store(tc.hubSkips)
			h.execExit["hub-pre"] = 75
			ur := updateWith(t, a, hubHooks("pre", "post"))
			if ur.GetOutcome() != tc.want || !strings.Contains(ur.GetError(), "skip") {
				t.Errorf("outcome %q (%s), want %s with the skip named", ur.GetOutcome(), ur.GetError(), tc.want)
			}
			h.requireOnly(t, "web", "nginx:1", true)
		})
	}
}
