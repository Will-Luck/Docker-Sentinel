package web

import (
	"context"

	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// remotePolicy is the update policy of a remote container as the scan
// applies it (engine.ResolvePolicy with the cluster's remote default): an
// override saved for its host-scoped key, else its sentinel.policy label,
// else auto for a :latest image when latest auto-update is on, else the
// cluster's default remote policy, else the global default.
func (s *Server) remotePolicy(rc RemoteContainer) string {
	key := store.ScopedKey(rc.HostID, rc.Name)
	if s.deps.Policy != nil {
		if p, ok := s.deps.Policy.GetPolicyOverride(key); ok {
			switch p {
			case "auto", "manual", "pinned":
				return p
			}
		}
	}
	if v, ok := rc.Labels["sentinel.policy"]; ok {
		switch v {
		case "auto", "manual", "pinned":
			return v
		}
	}
	if s.deps.Config != nil && s.deps.Config.LatestAutoUpdate() {
		if tag := registry.ExtractTag(rc.Image); tag == "latest" || tag == "" {
			return "auto"
		}
	}
	return s.remoteDefaultPolicy()
}

// remoteDefaultPolicy is the policy of a remote container with no
// override or label: the cluster's default remote policy when one is
// saved, else the global default.
func (s *Server) remoteDefaultPolicy() string {
	if s.deps.SettingsStore != nil {
		if v, err := s.deps.SettingsStore.LoadSetting(store.SettingClusterRemotePolicy); err == nil && v != "" {
			return v
		}
	}
	if s.deps.Config != nil && s.deps.Config.DefaultPolicy() != "" {
		return s.deps.Config.DefaultPolicy()
	}
	return "manual"
}

// RemoteCheck is the result of a registry check of a remote container.
type RemoteCheck struct {
	UpdateAvailable        bool
	NewerVersions          []string
	ResolvedCurrentVersion string
	ResolvedTargetVersion  string
	RemoteDigest           string
}

// RemoteRegistryChecker checks a remote container's image against the
// digest its agent reported, as the scan does: the hub's Docker daemon
// usually does not have the image.
type RemoteRegistryChecker interface {
	CheckRemoteForUpdate(ctx context.Context, imageRef, localDigest string, labels map[string]string) (RemoteCheck, error)
}

// ignoredFor filters check's newer versions by those ignored for key and
// reports whether nothing is left: every newer version, or the one update
// of a tag with no newer versions (by its resolved version or digest, as
// the scan keys it), is ignored.
func (s *Server) ignoredFor(key string, check *RemoteCheck) bool {
	if s.deps.IgnoredVersions == nil {
		return false
	}
	ignored, _ := s.deps.IgnoredVersions.GetIgnoredVersions(key)
	if len(ignored) == 0 {
		return false
	}
	set := make(map[string]bool, len(ignored))
	for _, v := range ignored {
		set[v] = true
	}
	if len(check.NewerVersions) == 0 {
		k := check.ResolvedTargetVersion
		if k == "" {
			k = check.RemoteDigest
		}
		return k != "" && set[k]
	}
	var kept []string
	for _, v := range check.NewerVersions {
		if !set[v] {
			kept = append(kept, v)
		}
	}
	check.NewerVersions = kept
	return len(kept) == 0
}
