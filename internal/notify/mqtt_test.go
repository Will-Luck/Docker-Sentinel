package notify

import "testing"

func TestNewMQTTQoSBounds(t *testing.T) {
	tests := []struct {
		name string
		qos  int
		want byte
	}{
		{"qos 0 preserved", 0, 0},
		{"qos 1 preserved", 1, 1},
		{"qos 2 preserved", 2, 2},
		{"qos 3 out of range", 3, 0},
		{"negative qos", -1, 0},
		{"overflow wrapping to valid byte", 257, 0},
		{"overflow wrapping to zero", 256, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMQTT("tcp://broker:1883", "topic", "", "", "", tt.qos)
			if m.qos != tt.want {
				t.Errorf("NewMQTT qos=%d: got QoS %d, want %d", tt.qos, m.qos, tt.want)
			}
		})
	}
}

func TestNewMQTTDefaultClientID(t *testing.T) {
	m := NewMQTT("tcp://broker:1883", "topic", "", "", "", 1)
	if m.clientID != "docker-sentinel" {
		t.Errorf("empty clientID: got %q, want %q", m.clientID, "docker-sentinel")
	}
}
