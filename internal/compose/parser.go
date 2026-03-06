package compose

import (
	"fmt"
	"os"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

// ServiceDeps maps service name to its dependency service names.
type ServiceDeps map[string][]string

// cache stores parsed compose files to avoid re-reading on every poll cycle.
// Key is the file path, value is the parsed deps + mod time.
type cacheEntry struct {
	deps    ServiceDeps
	modTime int64
}

var (
	cacheMu sync.RWMutex
	cache   = make(map[string]cacheEntry)
)

// ParseFile reads a Docker Compose file and extracts depends_on relationships.
// Results are cached by file path and modification time.
func ParseFile(path string) (ServiceDeps, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat compose file: %w", err)
	}

	modTime := info.ModTime().UnixNano()

	cacheMu.RLock()
	if entry, ok := cache[path]; ok && entry.modTime == modTime {
		cacheMu.RUnlock()
		return entry.deps, nil
	}
	cacheMu.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read compose file: %w", err)
	}

	deps, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	cacheMu.Lock()
	cache[path] = cacheEntry{deps: deps, modTime: modTime}
	cacheMu.Unlock()

	return deps, nil
}

// Parse extracts depends_on relationships from compose YAML content.
// Supports both short form (list of strings) and long form (map with conditions).
func Parse(data []byte) (ServiceDeps, error) {
	if len(data) == 0 {
		return ServiceDeps{}, nil
	}

	// Compose files have a "services" top-level key.
	// Each service may have "depends_on" as either:
	//   - A list of strings: depends_on: [db, redis]
	//   - A map with conditions: depends_on: { db: { condition: service_healthy } }
	var doc struct {
		Services map[string]struct {
			DependsOn interface{} `yaml:"depends_on"`
		} `yaml:"services"`
	}

	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse compose YAML: %w", err)
	}

	deps := make(ServiceDeps, len(doc.Services))
	for name, svc := range doc.Services {
		if svc.DependsOn == nil {
			continue
		}
		var serviceDeps []string
		switch v := svc.DependsOn.(type) {
		case []interface{}:
			// Short form: depends_on: [db, redis]
			for _, item := range v {
				if s, ok := item.(string); ok {
					serviceDeps = append(serviceDeps, s)
				}
			}
		case map[string]interface{}:
			// Long form: depends_on: { db: { condition: ... } }
			for dep := range v {
				serviceDeps = append(serviceDeps, dep)
			}
		}
		if len(serviceDeps) > 0 {
			sort.Strings(serviceDeps) // deterministic order
			deps[name] = serviceDeps
		}
	}

	return deps, nil
}

// ClearCache removes all cached entries. Useful for testing.
func ClearCache() {
	cacheMu.Lock()
	cache = make(map[string]cacheEntry)
	cacheMu.Unlock()
}
