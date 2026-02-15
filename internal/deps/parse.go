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

// ParseNetworkDependency extracts the container name from a "container:NAME" network mode.
func ParseNetworkDependency(networkMode string) string {
	if strings.HasPrefix(networkMode, "container:") {
		return strings.TrimPrefix(networkMode, "container:")
	}
	return ""
}
