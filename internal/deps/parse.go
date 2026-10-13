package deps

import (
	"strings"
)

// ParseDependsOn extracts dependencies from container labels.
// Supports:
//   - sentinel.depends-on: "container1,container2"
//   - com.docker.compose.depends_on: "svc1:service_started:true,svc2:service_healthy:true"
func ParseDependsOn(labels map[string]string) []string {
	var deps []string

	if v, ok := labels["sentinel.depends-on"]; ok && v != "" {
		for _, name := range strings.Split(v, ",") {
			if trimmed := strings.TrimSpace(name); trimmed != "" {
				deps = append(deps, trimmed)
			}
		}
	}

	if v, ok := labels["com.docker.compose.depends_on"]; ok && v != "" {
		for _, entry := range strings.Split(v, ",") {
			// Format: "service_name:condition:restart" or just "service_name"
			parts := strings.SplitN(strings.TrimSpace(entry), ":", 2)
			if name := strings.TrimSpace(parts[0]); name != "" {
				deps = append(deps, name)
			}
		}
	}

	return deps
}

// ComposeDependency is one entry of a compose depends_on label: the service
// depended on and whether compose restarts the dependent when it changes
// (`restart: true`, the third field of the entry).
type ComposeDependency struct {
	Service string
	Restart bool
}

// ParseComposeDependsOn parses com.docker.compose.depends_on, whose entries
// are "service:condition:restart" (older compose versions leave out the
// restart field, which then means false).
func ParseComposeDependsOn(labels map[string]string) []ComposeDependency {
	v := labels["com.docker.compose.depends_on"]
	if v == "" {
		return nil
	}
	var deps []ComposeDependency
	for _, entry := range strings.Split(v, ",") {
		parts := strings.Split(strings.TrimSpace(entry), ":")
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		restart := len(parts) >= 3 && strings.EqualFold(strings.TrimSpace(parts[2]), "true")
		deps = append(deps, ComposeDependency{Service: name, Restart: restart})
	}
	return deps
}

// ParseSentinelDependsOn parses sentinel.depends-on, a comma-separated list
// of container names.
func ParseSentinelDependsOn(labels map[string]string) []string {
	var deps []string
	for _, name := range strings.Split(labels["sentinel.depends-on"], ",") {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			deps = append(deps, trimmed)
		}
	}
	return deps
}

// ParseNetworkDependency extracts the container name from a "container:NAME" network mode.
func ParseNetworkDependency(networkMode string) string {
	if strings.HasPrefix(networkMode, "container:") {
		return strings.TrimPrefix(networkMode, "container:")
	}
	return ""
}
