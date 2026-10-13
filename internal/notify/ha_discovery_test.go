package notify

import (
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type fakeToken struct{}

func (fakeToken) Wait() bool                     { return true }
func (fakeToken) WaitTimeout(time.Duration) bool { return true }
func (fakeToken) Done() <-chan struct{}          { c := make(chan struct{}); close(c); return c }
func (fakeToken) Error() error                   { return nil }

type fakeBroker struct {
	mqtt.Client
	states map[string]string
}

func (f *fakeBroker) Publish(topic string, _ byte, _ bool, payload interface{}) mqtt.Token {
	if f.states == nil {
		f.states = map[string]string{}
	}
	if b, ok := payload.([]byte); ok {
		f.states[topic] = string(b)
	}
	return fakeToken{}
}
func (f *fakeBroker) IsConnected() bool { return true }
func (f *fakeBroker) Disconnect(uint)   {}

func TestSanitizeIDDoesNotCollide(t *testing.T) {
	seen := map[string]string{}
	for _, n := range []string{"my-app", "my.app", "my_app", "my app", "plain", "plain2"} {
		id := sanitizeID(n)
		if prev, ok := seen[id]; ok {
			t.Fatalf("%q and %q both map to %q", prev, n, id)
		}
		seen[id] = n
	}
	if got := sanitizeID("plain"); got != "plain" {
		t.Errorf("plain names must keep their ID, got %q", got)
	}
}

func TestHADiscoveryClearsSensorWhenNoLongerPending(t *testing.T) {
	fb := &fakeBroker{}
	h := &HADiscovery{broker: fb, prefix: "homeassistant", baseTopic: "sentinel", active: map[string]bool{}}
	if err := h.SyncContainerStates([]string{"web", "db"}); err != nil {
		t.Fatal(err)
	}
	if err := h.SyncContainerStates([]string{"db"}); err != nil {
		t.Fatal(err)
	}
	if got := fb.states["sentinel/containers/web/update_available"]; got != "OFF" {
		t.Fatalf("web sensor = %q, want OFF", got)
	}
	if got := fb.states["sentinel/containers/db/update_available"]; got != "ON" {
		t.Fatalf("db sensor = %q, want ON", got)
	}
}

func TestHADiscoveryDisabledIsNoOp(t *testing.T) {
	h := &HADiscovery{}
	if err := h.SyncContainerStates([]string{"web"}); err != nil {
		t.Fatal(err)
	}
	if err := h.PublishPendingCount(3); err != nil {
		t.Fatal(err)
	}
}
