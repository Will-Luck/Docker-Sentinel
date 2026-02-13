package engine

import (
	"fmt"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// PolicySource indicates where a resolved policy came from.
type PolicySource string

const (
	SourceOverride PolicySource = "override" // BoltDB
	SourceLabel    PolicySource = "label"    // Docker label
	SourceLatest   PolicySource = "latest"   // :latest tag auto-policy
	SourceDefault  PolicySource = "default"  // Global config
)

// ResolvedPolicy is the effective policy for a container with its source.
type ResolvedPolicy struct {
	Policy string       `json:"policy"`
	Source PolicySource `json:"source"`
}

// ResolvePolicy returns the effective policy for a container.
// Precedence: DB override → Docker label → latest-tag auto → default.
func ResolvePolicy(db *store.Store, labels map[string]string, name, imageTag, defaultPolicy string, latestAutoUpdate bool) ResolvedPolicy {
	if p, ok := db.GetPolicyOverride(name); ok {
		switch p {
		case "auto", "manual", "pinned":
			return ResolvedPolicy{Policy: p, Source: SourceOverride}
		}
	}

	p, fromLabel := docker.ContainerPolicy(labels, defaultPolicy)
	if fromLabel {
		return ResolvedPolicy{Policy: string(p), Source: SourceLabel}
	}

	// Optionally auto-update :latest containers regardless of default policy.
	if latestAutoUpdate && (imageTag == "latest" || imageTag == "") {
		return ResolvedPolicy{Policy: "auto", Source: SourceLatest}
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
