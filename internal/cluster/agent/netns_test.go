package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// netnsHost has a provider "vpn" on app:1 (app:2 in the registry) and a
// running consumer "web" in its network namespace, referencing it by ref.
// ref is a function of the provider so a test can use its ID or name.
func netnsHost(t *testing.T, ref func(p *fakeContainer) string) (*fakeHost, *fakeContainer, *fakeContainer) {
	t.Helper()
	h := fidelityHost(t)
	provider := h.addFromImage(t, "vpn", "app:1", nil, &container.HostConfig{}, nil, true)
	consumer := h.addFromImage(t, "web", "app:1", nil,
		&container.HostConfig{NetworkMode: container.NetworkMode("container:" + ref(provider))}, nil, false)
	if err := h.StartContainer(context.Background(), consumer.id); err != nil {
		t.Fatal(err)
	}
	return h, provider, consumer
}

// A consumer created against the provider's ID (compose writes the full
// ID) is cut off when the provider is replaced: it keeps only loopback, and
// restarting it fails because the old ID is gone. The agent must recreate
// it in the new provider's namespace, as the local engine does.
func TestRecreateContainer_ProviderUpdateRecreatesItsConsumers(t *testing.T) {
	h, oldProvider, oldConsumer := netnsHost(t, func(p *fakeContainer) string { return p.id })
	a := newTestAgent(t.TempDir(), h)

	if _, err := a.recreateContainer(context.Background(), "vpn", "app:2"); err != nil {
		t.Fatalf("recreateContainer: %v", err)
	}
	provider, consumer := h.byName("vpn"), h.byName("web")
	if provider.id == oldProvider.id {
		t.Fatal("test setup: provider was not replaced")
	}
	if consumer == nil || !consumer.running {
		t.Fatalf("consumer web not running after the provider update: %+v", consumer)
	}
	if consumer.joined != provider.id {
		t.Errorf("consumer joined %s, want the new provider %s (old was %s)", consumer.joined, provider.id, oldProvider.id)
	}
	if mode := string(consumer.hostCfg.NetworkMode); mode != "container:"+provider.id {
		t.Errorf("consumer network mode = %s, want container:%s", mode, provider.id)
	}
	if consumer.id == oldConsumer.id {
		t.Error("consumer was not recreated")
	}
	if got := h.names(); !slices.Equal(got, []string{"vpn", "web"}) {
		t.Errorf("containers on host = %v, want vpn and web only", got)
	}
}

// A consumer that names its provider follows the name: restarting it is
// enough to join the new provider, and it keeps its own config.
func TestRecreateContainer_ProviderUpdateRestartsConsumersThatUseItsName(t *testing.T) {
	h, _, oldConsumer := netnsHost(t, func(*fakeContainer) string { return "vpn" })
	a := newTestAgent(t.TempDir(), h)

	if _, err := a.recreateContainer(context.Background(), "vpn", "app:2"); err != nil {
		t.Fatalf("recreateContainer: %v", err)
	}
	provider, consumer := h.byName("vpn"), h.byName("web")
	if consumer.id != oldConsumer.id {
		t.Error("a name-referencing consumer was recreated; a restart is enough")
	}
	if !consumer.running || consumer.joined != provider.id {
		t.Errorf("consumer running=%v joined %s, want running in the new provider %s", consumer.running, consumer.joined, provider.id)
	}
	if mode := string(consumer.hostCfg.NetworkMode); mode != "container:vpn" {
		t.Errorf("consumer network mode = %s, want container:vpn kept", mode)
	}
}

// A consumer whose own update is running is left to that update.
func TestRecreateContainer_ProviderUpdateSkipsAConsumerBeingUpdated(t *testing.T) {
	h, _, oldConsumer := netnsHost(t, func(p *fakeContainer) string { return p.id })
	a := newTestAgent(t.TempDir(), h)
	a.beginUpdate("web")
	defer a.endUpdate("web")

	if _, err := a.recreateContainer(context.Background(), "vpn", "app:2"); err != nil {
		t.Fatalf("recreateContainer: %v", err)
	}
	if c := h.byName("web"); c == nil || c.id != oldConsumer.id {
		t.Error("consumer under its own update was recreated")
	}
}

// When the provider's update fails after it was stopped, the restored
// provider is the same container again; its consumers lost the namespace
// when it stopped and must be restarted into it.
func TestRecreateContainer_RestoredProviderRestartsItsConsumers(t *testing.T) {
	h, oldProvider, oldConsumer := netnsHost(t, func(p *fakeContainer) string { return p.id })
	h.startErr["app:2"] = errors.New("port is already allocated")
	a := newTestAgent(t.TempDir(), h)

	if _, err := a.recreateContainer(context.Background(), "vpn", "app:2"); err == nil {
		t.Fatal("recreateContainer returned no error although start failed")
	}
	consumer := h.byName("web")
	if h.byName("vpn").id != oldProvider.id {
		t.Fatal("test setup: provider not restored")
	}
	if consumer.id != oldConsumer.id || !slices.Contains(h.restartCalls, consumer.id) || !consumer.running || consumer.joined != oldProvider.id {
		t.Errorf("consumer id=%s restarted=%v running=%v joined=%s, want the same consumer restarted into %s",
			consumer.id, slices.Contains(h.restartCalls, consumer.id), consumer.running, consumer.joined, oldProvider.id)
	}
}
