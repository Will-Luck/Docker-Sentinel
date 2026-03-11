package web

import "testing"

func TestIsLocalSocketEndpoint(t *testing.T) {
	tests := []struct {
		name string
		ep   PortainerEndpoint
		want bool
	}{
		{"unix socket", PortainerEndpoint{URL: "unix:///var/run/docker.sock"}, true},
		{"empty URL docker type", PortainerEndpoint{URL: "", Type: 1}, true},
		{"tcp endpoint", PortainerEndpoint{URL: "tcp://192.168.1.61:2375", Type: 1}, false},
		{"empty URL non-docker", PortainerEndpoint{URL: "", Type: 2}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLocalSocketEndpoint(tt.ep); got != tt.want {
				t.Errorf("isLocalSocketEndpoint() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsLocalPortainerInstance(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"localhost https", "https://localhost:9443", true},
		{"127.0.0.1", "https://127.0.0.1:9443", true},
		{"empty url", "", false},
		{"invalid url", "://bad", false},
		// Remote IPs should return false (unless they happen to be local).
		{"remote IP", "https://203.0.113.99:9443", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLocalPortainerInstance(tt.url)
			if got != tt.want {
				t.Errorf("isLocalPortainerInstance(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}
