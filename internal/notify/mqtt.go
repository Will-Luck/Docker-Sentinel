package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MQTTSettings holds configuration for an MQTT notification channel.
type MQTTSettings struct {
	Broker   string `json:"broker"`
	Topic    string `json:"topic"`
	ClientID string `json:"client_id,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	QoS      int    `json:"qos,omitempty"`
}

// MQTT sends notifications by publishing JSON messages to an MQTT broker.
type MQTT struct {
	broker   string
	topic    string
	clientID string
	username string
	password string
	qos      byte
}

// NewMQTT creates an MQTT notifier.
func NewMQTT(broker, topic, clientID, username, password string, qos int) *MQTT {
	q := byte(qos)
	if q > 2 {
		q = 0
	}
	if clientID == "" {
		clientID = "docker-sentinel"
	}
	return &MQTT{
		broker:   broker,
		topic:    topic,
		clientID: clientID,
		username: username,
		password: password,
		qos:      q,
	}
}

// Name returns the provider name for logging.
func (m *MQTT) Name() string { return "mqtt" }

// Send publishes an event as a JSON payload to the configured MQTT topic.
func (m *MQTT) Send(ctx context.Context, event Event) error {
	opts := mqtt.NewClientOptions().
		SetClientID(m.clientID).
		AddBroker(m.broker).
		SetConnectTimeout(10 * time.Second).
		SetWriteTimeout(10 * time.Second)
	if m.username != "" {
		opts.SetUsername(m.username)
		opts.SetPassword(m.password)
	}

	client := mqtt.NewClient(opts)
	tok := client.Connect()
	if !tok.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("mqtt connect timeout")
	}
	if tok.Error() != nil {
		return fmt.Errorf("mqtt connect: %w", tok.Error())
	}
	defer client.Disconnect(250)

	payload := mqttPayload{
		Type:          string(event.Type),
		ContainerName: event.ContainerName,
		OldImage:      event.OldImage,
		NewImage:      event.NewImage,
		Error:         event.Error,
		Timestamp:     event.Timestamp.UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal mqtt payload: %w", err)
	}

	pub := client.Publish(m.topic, m.qos, false, body)
	if !pub.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("mqtt publish timeout")
	}
	if pub.Error() != nil {
		return fmt.Errorf("mqtt publish: %w", pub.Error())
	}
	return nil
}

type mqttPayload struct {
	Type          string `json:"type"`
	ContainerName string `json:"container_name"`
	OldImage      string `json:"old_image,omitempty"`
	NewImage      string `json:"new_image,omitempty"`
	Error         string `json:"error,omitempty"`
	Timestamp     string `json:"timestamp"`
}
