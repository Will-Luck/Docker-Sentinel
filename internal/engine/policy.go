package engine

import (
	"fmt"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// PolicySource indicates where a resolved policy came from.
type PolicySource string

const (
	SourceOverride PolicySource = "override" // BoltDB
	SourceLabel    PolicySource = "label"    // Docker label
	SourceDefault  PolicySource = "default"  // Global config
)

// ResolvedPolicy is the effective policy for a container with its source.
type ResolvedPolicy struct {
	Policy string       `json:"policy"`
	Source PolicySource `json:"source"`
}

// ResolvePolicy returns the effective policy for a container.
// Precedence: DB override → Docker label → default.
func ResolvePolicy(db *store.Store, labels map[string]string, name, defaultPolicy string) ResolvedPolicy {
	if p, ok := db.GetPolicyOverride(name); ok {
		switch p {
		case "auto", "manual", "pinned":
			return ResolvedPolicy{Policy: p, Source: SourceOverride}
		}
	}

	p := docker.ContainerPolicy(labels, defaultPolicy)
	if string(p) != defaultPolicy {
		return ResolvedPolicy{Policy: string(p), Source: SourceLabel}
	}

	return ResolvedPolicy{Policy: defaultPolicy, Source: SourceDefault}
}

// ValidatePolicy checks that a policy string is valid.
func ValidatePolicy(policy string) error {
	switch policy {
	case "auto", "manual", "pinned":
		return nil
	default:
		return fmt.Errorf("invalid policy: %q (must be auto, manual, or pinned)", policy)
	}
}

// findContainerID searches containers for one matching the given name and
// returns its ID. Returns empty string if not found.
func findContainerID(containers []container.Summary, name string) string {
	for _, c := range containers {
		if containerName(c) == name {
			return c.ID
		}
	}
	return ""
}
