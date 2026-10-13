package web

import "testing"

// Approving a digest-only update for a remote container must send the
// agent a real image reference: agents before v2.17.0 fail on an empty one.
func TestRemoteUpdateTarget(t *testing.T) {
	tests := []struct {
		current, newer, want string
	}{
		{"nginx:latest", "", "nginx:latest"},
		{"nginx:1.25", "nginx:1.26", "nginx:1.26"},
	}
	for _, tt := range tests {
		if got := remoteUpdateTarget(tt.current, tt.newer); got != tt.want {
			t.Errorf("remoteUpdateTarget(%q, %q) = %q, want %q", tt.current, tt.newer, got, tt.want)
		}
	}
}
