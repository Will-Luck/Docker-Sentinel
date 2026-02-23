package notify

import (
	"encoding/json"
	"fmt"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// HADiscovery publishes Home Assistant MQTT auto-discovery payloads.
type HADiscovery struct {
	broker    mqtt.Client
	prefix    string // HA discovery prefix, default "homeassistant"
	baseTopic string // state topic prefix, default "sentinel"
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
	prefix := cfg.Prefix
	if prefix == "" {
		prefix = "homeassistant"
	}

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID + "-ha").
		SetConnectTimeout(10 * time.Second).
		SetAutoReconnect(true).
		SetCleanSession(true)

	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.WaitTimeout(10*time.Second) && token.Error() != nil {
		return nil, fmt.Errorf("ha discovery mqtt connect: %w", token.Error())
	}

	return &HADiscovery{
		broker:    client,
		prefix:    prefix,
		baseTopic: "sentinel",
	}, nil
}

// Close disconnects the MQTT client.
func (h *HADiscovery) Close() {
	if h.broker != nil && h.broker.IsConnected() {
		h.broker.Disconnect(1000)
	}
}

// PublishContainerState publishes a binary_sensor discovery config + state for a container.
func (h *HADiscovery) PublishContainerState(name string, updateAvailable bool) error {
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

func sanitizeID(s string) string {
	var b []byte
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b = append(b, c)
		} else {
			b = append(b, '_')
		}
	}
	return string(b)
}
