package notify

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// HADiscovery publishes Home Assistant MQTT auto-discovery payloads.
//
// The zero value is a disabled publisher: every method is a no-op until
// Reconfigure connects it, so the settings toggle can switch it on and off
// without a restart.
type HADiscovery struct {
	mu        sync.Mutex
	broker    mqtt.Client
	prefix    string // HA discovery prefix, default "homeassistant"
	baseTopic string // state topic prefix, default "sentinel"
	active    map[string]bool
}

// HADiscoveryConfig holds the configuration for HA discovery.
type HADiscoveryConfig struct {
	Broker   string
	ClientID string
	Username string
	Password string
	Prefix   string // default "homeassistant"
}

// NewHADiscovery creates and connects an HA discovery publisher.
func NewHADiscovery(cfg HADiscoveryConfig) (*HADiscovery, error) {
	h := &HADiscovery{}
	if err := h.Reconfigure(&cfg); err != nil {
		return nil, err
	}
	return h, nil
}

// Reconfigure disconnects any current broker and, when cfg is not nil,
// connects to the new one. A nil cfg disables publishing.
func (h *HADiscovery) Reconfigure(cfg *HADiscoveryConfig) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closeLocked()
	if cfg == nil {
		return nil
	}
	prefix := cfg.Prefix
	if prefix == "" {
		prefix = "homeassistant"
	}

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID + "-ha").
		SetConnectTimeout(10 * time.Second).
		SetAutoReconnect(true).
		SetCleanSession(false)

	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}

	client := mqtt.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("ha discovery mqtt connect: timeout after 10s")
	}
	if token.Error() != nil {
		return fmt.Errorf("ha discovery mqtt connect: %w", token.Error())
	}

	h.broker = client
	h.prefix = prefix
	h.baseTopic = "sentinel"
	h.active = make(map[string]bool)
	return nil
}

// Close disconnects the MQTT client.
func (h *HADiscovery) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closeLocked()
}

func (h *HADiscovery) closeLocked() {
	if h.broker != nil && h.broker.IsConnected() {
		h.broker.Disconnect(1000)
	}
	h.broker = nil
}

// SyncContainerStates publishes ON for every name in pending and OFF for
// every container it published ON for earlier that is no longer pending, so
// a sensor clears once its update is applied, rejected or dismissed.
func (h *HADiscovery) SyncContainerStates(pending []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.broker == nil {
		return nil
	}
	now := make(map[string]bool, len(pending))
	var firstErr error
	for _, name := range pending {
		now[name] = true
		if err := h.publishContainerStateLocked(name, true); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for name := range h.active {
		if now[name] {
			continue
		}
		if err := h.publishContainerStateLocked(name, false); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	h.active = now
	return firstErr
}

// PublishContainerState publishes a binary_sensor discovery config + state for a container.
func (h *HADiscovery) PublishContainerState(name string, updateAvailable bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.publishContainerStateLocked(name, updateAvailable)
}

func (h *HADiscovery) publishContainerStateLocked(name string, updateAvailable bool) error {
	if h.broker == nil {
		return nil
	}
	safeID := sanitizeID(name)

	configTopic := fmt.Sprintf("%s/binary_sensor/sentinel_%s/config", h.prefix, safeID)
	stateTopic := fmt.Sprintf("%s/containers/%s/update_available", h.baseTopic, safeID)

	config := map[string]interface{}{
		"name":         fmt.Sprintf("Sentinel %s Update", name),
		"unique_id":    fmt.Sprintf("sentinel_%s_update", safeID),
		"state_topic":  stateTopic,
		"payload_on":   "ON",
		"payload_off":  "OFF",
		"device_class": "update",
		"device": map[string]interface{}{
			"identifiers":  []string{"docker_sentinel"},
			"name":         "Docker Sentinel",
			"manufacturer": "Docker Sentinel",
			"model":        "Container Update Monitor",
		},
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return err
	}

	// Publish config retained so HA picks it up on restart.
	if token := h.broker.Publish(configTopic, 1, true, configJSON); token.WaitTimeout(5*time.Second) && token.Error() != nil {
		return token.Error()
	}

	state := "OFF"
	if updateAvailable {
		state = "ON"
	}
	if token := h.broker.Publish(stateTopic, 1, true, []byte(state)); token.WaitTimeout(5*time.Second) && token.Error() != nil {
		return token.Error()
	}

	return nil
}

// PublishPendingCount publishes a sensor with the total pending update count.
func (h *HADiscovery) PublishPendingCount(count int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.broker == nil {
		return nil
	}
	configTopic := fmt.Sprintf("%s/sensor/sentinel_pending/config", h.prefix)
	stateTopic := fmt.Sprintf("%s/pending_count", h.baseTopic)

	config := map[string]interface{}{
		"name":                "Sentinel Pending Updates",
		"unique_id":           "sentinel_pending_updates",
		"state_topic":         stateTopic,
		"unit_of_measurement": "updates",
		"icon":                "mdi:docker",
		"device": map[string]interface{}{
			"identifiers":  []string{"docker_sentinel"},
			"name":         "Docker Sentinel",
			"manufacturer": "Docker Sentinel",
			"model":        "Container Update Monitor",
		},
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return err
	}

	if token := h.broker.Publish(configTopic, 1, true, configJSON); token.WaitTimeout(5*time.Second) && token.Error() != nil {
		return token.Error()
	}

	if token := h.broker.Publish(stateTopic, 1, true, []byte(fmt.Sprintf("%d", count))); token.WaitTimeout(5*time.Second) && token.Error() != nil {
		return token.Error()
	}

	return nil
}

// sanitizeID turns a container name into an MQTT topic and unique_id safe
// token. Names that needed any character replaced get a short hash of the
// original appended, so "my-app", "my.app" and "my_app" stay distinct.
func sanitizeID(s string) string {
	var b []byte
	changed := false
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b = append(b, c)
		} else {
			b = append(b, '_')
			changed = true
		}
	}
	if !changed {
		return string(b)
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%s_%08x", b, h.Sum32())
}
