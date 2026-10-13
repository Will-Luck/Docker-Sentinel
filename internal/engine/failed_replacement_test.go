package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// When an undo cannot remove the failed replacement, the replacement is set
// aside (renamed, its restart policy off) and the old container gets its
// name back and runs, instead of the service staying down until recovery.
// The journal keeps the set-aside name, scans leave that container alone,
// and recovery removes it once the daemon lets it.
func TestUndoSetsAsideAReplacementItCannotRemove(t *testing.T) {
	f, u, old := setupFakeUpdate(t, true, &container.HostConfig{RestartPolicy: always})
	f.failStart["app-1"] = errors.New("OCI error")
	f.failRemove = map[string]error{"app-1": errors.New("device or resource busy")}

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the update to fail")
	}

	if old.name != "app" || !old.running || old.host.RestartPolicy != always {
		t.Fatalf("containers = %v policy = %+v, want the old container back as app, running", f.names(), old.host.RestartPolicy)
	}
	failed := f.find("app-1")
	if failed == nil || failed.name != "app-sentinel-failed" || failed.running || failed.host.RestartPolicy.Name != container.RestartPolicyDisabled {
		t.Fatalf("containers = %v, want the replacement set aside as app-sentinel-failed, stopped, policy off", f.names())
	}
	recs := journal(t, u)
	if len(recs) != 1 || recs[0].FailedName != "app-sentinel-failed" {
		t.Fatalf("journal = %+v, want the set-aside name kept for recovery", recs)
	}
	if !u.setAside("app-sentinel-failed") {
		t.Error("scans would treat the set-aside replacement as a container of its own")
	}

	delete(f.failRemove, "app-1")
	u.resolvePendingSwaps(context.Background())
	if f.find("app-1") != nil || len(journal(t, u)) != 0 || !old.running || old.name != "app" {
		t.Errorf("containers = %v journal = %+v, want the failed replacement removed and the old container untouched", f.names(), journal(t, u))
	}
}

// The set-aside name gets a suffix when it is taken.
func TestUndoSetsAsideUnderAFreeName(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	f.add("other", "app-sentinel-failed", "app:latest", false, nil, nil)
	f.failStart["app-1"] = errors.New("OCI error")
	f.failRemove = map[string]error{"app-1": errors.New("device or resource busy")}

	_ = u.UpdateContainer(context.Background(), "old", "app", "")

	failed := f.find("app-1")
	if failed == nil || !strings.HasPrefix(failed.name, "app-sentinel-failed-") {
		t.Fatalf("containers = %v, want the replacement set aside under a suffixed name", f.names())
	}
	if c := f.byName("app"); c == nil || c.id != "old" || !c.running {
		t.Errorf("containers = %v, want the old container running as app", f.names())
	}
}
