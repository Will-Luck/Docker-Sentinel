package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// Finalise recreates the validated container from its tag. If the tag
// moved in between (another container on the same tag was pulled), the
// recreate would swap in an image that was never validated.
func TestFinaliseContainerRefusesMovedTag(t *testing.T) {
	f := newFakeDocker()
	f.imageIDs["app:latest"] = "sha256:validated"
	n1 := f.add("n1", "app", "app:latest", true, &container.Config{Labels: map[string]string{"sentinel.maintenance": "true"}}, nil)
	f.imageIDs["app:latest"] = "sha256:newer" // the tag moved after n1 was created
	u := newFakeUpdater(t, f)

	_, err := u.finaliseContainer(context.Background(), "n1", "app")

	var fErr *finaliseError
	if !errors.As(err, &fErr) || fErr.stage != "verify" || fErr.down || fErr.stuck {
		t.Fatalf("err = %v, want a verify-stage finalise error with the container in place", err)
	}
	if c := f.byName("app"); c != n1 || !c.running || f.runningWithName("app") != 1 {
		t.Errorf("containers = %v, want the validated container kept as app", f.names())
	}
}

// The same check guards the recreate of a network-namespace consumer.
func TestRecreateNamespaceConsumerRefusesMovedTag(t *testing.T) {
	f := newFakeDocker()
	f.imageIDs["qbit:4"] = "sha256:c1"
	f.add("vpn", "gluetun", "gluetun:latest", true, nil, nil)
	f.add("q1", "qbit", "qbit:4", true, nil, &container.HostConfig{NetworkMode: "container:old-vpn"})
	f.imageIDs["qbit:4"] = "sha256:c2"
	u := newFakeUpdater(t, f)
	consumer, _ := f.InspectContainer(context.Background(), "q1")

	u.recreateNamespaceConsumer(context.Background(), "q1", "qbit", consumer, "vpn", "gluetun")

	if c := f.byName("qbit"); c == nil || c.id != "q1" || c.imageID != "sha256:c1" {
		t.Errorf("containers = %v, want the consumer kept on its own image", f.names())
	}
}
