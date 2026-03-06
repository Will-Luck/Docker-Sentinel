package compose

import (
	"log/slog"
	"strings"
)

// Docker labels used to identify compose-managed containers.
const (
	// ConfigFilesLabel stores the compose file path(s) used to create the container.
	// Value is a comma-separated list of absolute paths.
	ConfigFilesLabel = "com.docker.compose.project.config_files"

	// ServiceLabel stores the compose service name for this container.
	ServiceLabel = "com.docker.compose.service"

	// ProjectLabel stores the compose project name.
	ProjectLabel = "com.docker.compose.project"
)

// DiscoverPaths extracts compose file paths from container labels.
// Returns nil if the container wasn't created by Docker Compose.
func DiscoverPaths(labels map[string]string) []string {
	raw, ok := labels[ConfigFilesLabel]
	if !ok || raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var paths []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// ServiceName extracts the compose service name from container labels.
// Returns empty string if the container wasn't created by Docker Compose.
func ServiceName(labels map[string]string) string {
	return labels[ServiceLabel]
}

// ProjectName extracts the compose project name from container labels.
func ProjectName(labels map[string]string) string {
	return labels[ProjectLabel]
}

// DiscoverDeps reads compose files referenced by container labels and returns
// the dependencies for the specific service this container represents.
// Falls back gracefully: if the compose file can't be read, returns nil (not an error).
func DiscoverDeps(labels map[string]string) []string {
	svcName := ServiceName(labels)
	if svcName == "" {
		return nil
	}

	paths := DiscoverPaths(labels)
	if len(paths) == 0 {
		return nil
	}

	// Parse all referenced compose files and merge deps.
	// Multiple files is common (docker-compose.yml + docker-compose.override.yml).
	var allDeps []string
	seen := make(map[string]bool)

	for _, path := range paths {
		fileDeps, err := ParseFile(path)
		if err != nil {
			slog.Debug("compose: failed to parse file", "path", path, "err", err)
			continue
		}
		for _, dep := range fileDeps[svcName] {
			if !seen[dep] {
				seen[dep] = true
				allDeps = append(allDeps, dep)
			}
		}
	}

	return allDeps
}

// MergeDeps merges compose-derived dependencies with label-based ones.
// Label-based deps (from sentinel.depends-on) take priority: if a service
// has explicit label deps, compose deps for that service are ignored.
func MergeDeps(labelDeps, composeDeps ServiceDeps) ServiceDeps {
	merged := make(ServiceDeps, len(labelDeps)+len(composeDeps))

	// Start with compose deps as the base layer.
	for name, deps := range composeDeps {
		merged[name] = append([]string{}, deps...)
	}

	// Label deps override compose deps entirely per service.
	for name, deps := range labelDeps {
		merged[name] = deps
	}

	return merged
}
