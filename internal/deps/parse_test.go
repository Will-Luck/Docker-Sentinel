package deps

import (
	"reflect"
	"testing"
)

func TestParseSentinelLabel(t *testing.T) {
	labels := map[string]string{
		"sentinel.depends-on": "postgres,redis",
	}
	deps := ParseDependsOn(labels)
	expected := []string{"postgres", "redis"}
	if !reflect.DeepEqual(deps, expected) {
		t.Errorf("got %v, want %v", deps, expected)
	}
}

func TestParseComposeLabel(t *testing.T) {
	labels := map[string]string{
		"com.docker.compose.depends_on": "db:service_started:true,cache:service_healthy:true",
	}
	deps := ParseDependsOn(labels)
	expected := []string{"db", "cache"}
	if !reflect.DeepEqual(deps, expected) {
		t.Errorf("got %v, want %v", deps, expected)
	}
}

func TestParseBothLabels(t *testing.T) {
	labels := map[string]string{
		"sentinel.depends-on":           "explicit-dep",
		"com.docker.compose.depends_on": "compose-dep:service_started:true",
	}
	deps := ParseDependsOn(labels)
	if len(deps) != 2 {
		t.Errorf("expected 2 deps, got %d: %v", len(deps), deps)
	}
}

func TestParseNetworkDependency(t *testing.T) {
	tests := []struct {
		mode string
		want string
	}{
		{"container:vpn", "vpn"},
		{"bridge", ""},
		{"host", ""},
		{"container:", ""},
	}
	for _, tt := range tests {
		got := ParseNetworkDependency(tt.mode)
		if got != tt.want {
			t.Errorf("ParseNetworkDependency(%q) = %q, want %q", tt.mode, got, tt.want)
		}
	}
}

func TestParseEmptyLabels(t *testing.T) {
	deps := ParseDependsOn(map[string]string{})
	if len(deps) != 0 {
		t.Errorf("expected no deps, got %v", deps)
	}
}
