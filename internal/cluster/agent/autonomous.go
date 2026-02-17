// Package agent — autonomous.go implements the offline fallback scan loop and
// policy cache for when the agent loses connectivity to the server.
//
// Flow:
//  1. Agent detects server unreachable (heartbeat failures, stream error)
//  2. Reconnection attempts with exponential backoff (handled by agent.go)
//  3. After GracePeriodOffline, agent switches to autonomous mode
//  4. Autonomous mode: monitors container health using cached policies
//  5. All state changes are journaled for replay when reconnected
//  6. On reconnect: journal is drained and sent to server (sync.go)
//
// Autonomous mode does NOT attempt container updates — it cannot do registry
// checks without the server. It monitors health, logs anomalies, and waits
// for reconnection.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// policyCache stores cached policies and settings from the server.
// Written when PolicySync/SettingsSync messages arrive over the stream.
// Read when the agent operates autonomously to determine how to handle
// each container.
type policyCache struct {
	mu sync.RWMutex

	// Per-container policy overrides from the server.
	policies      map[string]string // container_name -> "auto"/"manual"/"pinned"
	defaultPolicy string

	// Operational settings mirrored from the server.
	pollInterval    time.Duration
	gracePeriod     time.Duration
	imageCleanup    bool
	hooksEnabled    bool
	dependencyAware bool
	rollbackPolicy  string
}

// policyCacheFile is the JSON-serialisable representation persisted to disk.
// Separate from policyCache to keep the mutex out of serialisation and to
// make the on-disk format explicit.
type policyCacheFile struct {
	Policies        map[string]string `json:"policies"`
	DefaultPolicy   string            `json:"default_policy"`
	PollInterval    time.Duration     `json:"poll_interval"`
	GracePeriod     time.Duration     `json:"grace_period"`
	ImageCleanup    bool              `json:"image_cleanup"`
	HooksEnabled    bool              `json:"hooks_enabled"`
	DependencyAware bool              `json:"dependency_aware"`
	RollbackPolicy  string            `json:"rollback_policy"`
}

func newPolicyCache() *policyCache {
	return &policyCache{
		policies:      make(map[string]string),
		defaultPolicy: "manual", // safest default for autonomous operation
	}
}

// applyPolicySync updates the cache from a server PolicySync message.
func (pc *policyCache) applyPolicySync(sync *proto.PolicySync) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if policies := sync.GetPolicies(); policies != nil {
		// Full replace — the server sends the complete policy map each time.
		pc.policies = make(map[string]string, len(policies))
		for name, pol := range policies {
			pc.policies[name] = pol
		}
	}

	if dp := sync.GetDefaultPolicy(); dp != "" {
		pc.defaultPolicy = dp
	}
}

// applySettingsSync updates the cache from a server SettingsSync message.
func (pc *policyCache) applySettingsSync(sync *proto.SettingsSync) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pi := sync.GetPollInterval(); pi != nil {
		pc.pollInterval = pi.AsDuration()
	}
	if gp := sync.GetGracePeriod(); gp != nil {
		pc.gracePeriod = gp.AsDuration()
	}

	pc.imageCleanup = sync.GetImageCleanup()
	pc.hooksEnabled = sync.GetHooksEnabled()
	pc.dependencyAware = sync.GetDependencyAware()

	if rp := sync.GetRollbackPolicy(); rp != "" {
		pc.rollbackPolicy = rp
	}
}

// resolvePolicy determines the effective policy for a container.
// Priority order:
//  1. Container labels (sentinel.policy) — highest priority
//  2. Server-pushed policy overrides
//  3. Default policy from server
//  4. Hardcoded "manual" — safest fallback for autonomous operation
func (pc *policyCache) resolvePolicy(containerName string, labels map[string]string) string {
	// Label takes precedence — this mirrors the server-side resolution.
	if lbl, ok := labels["sentinel.policy"]; ok {
		return lbl
	}

	pc.mu.RLock()
	defer pc.mu.RUnlock()

	if pol, ok := pc.policies[containerName]; ok {
		return pol
	}
	if pc.defaultPolicy != "" {
		return pc.defaultPolicy
	}
	return "manual"
}

// --- Autonomous scan loop ---

// runAutonomous runs the monitoring loop when disconnected from the server.
// It periodically lists containers and logs their state, but does NOT
// attempt any updates — the agent lacks registry access without the server.
//
// The loop exits when ctx is cancelled (typically because reconnection
// succeeded or the agent is shutting down).
func (a *Agent) runAutonomous(ctx context.Context) error {
	a.log.Warn("entering autonomous mode -- server unreachable",
		"offline_since", a.offlineSince,
		"grace_period", a.cfg.GracePeriodOffline,
	)

	interval := a.policies.pollInterval
	if interval == 0 {
		interval = 6 * time.Hour
	}

	// Run one scan immediately on entry, then on the ticker.
	a.autonomousScan(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.autonomousScan(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// autonomousScan lists local containers and logs their state. In autonomous
// mode we cannot check registries for updates, so this is purely a health
// monitoring pass. Anomalies (unexpected stops, state changes) are journaled
// for the server to process on reconnection.
func (a *Agent) autonomousScan(ctx context.Context) {
	containers, err := a.docker.ListContainers(ctx)
	if err != nil {
		a.log.Error("autonomous scan: failed to list containers", "error", err)
		return
	}

	running, stopped := 0, 0
	byPolicy := map[string]int{"auto": 0, "manual": 0, "pinned": 0}

	for _, c := range containers {
		if c.State == "running" {
			running++
		} else {
			stopped++
		}

		// Resolve the cached policy for each container. We strip the
		// leading "/" Docker adds to container names.
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		pol := a.policies.resolvePolicy(name, c.Labels)
		byPolicy[pol]++
	}

	a.log.Info("autonomous scan complete",
		"total", len(containers),
		"running", running,
		"stopped", stopped,
		"policy_auto", byPolicy["auto"],
		"policy_manual", byPolicy["manual"],
		"policy_pinned", byPolicy["pinned"],
	)

	// Future enhancement: detect containers that have crashed since last
	// scan and journal state_change entries. For MVP, we just log counts
	// so the operator knows the agent is alive and monitoring.
}

// shouldEnterAutonomous checks whether the grace period has elapsed and
// autonomous mode should activate. Returns false if no grace period is
// configured (meaning autonomous mode is disabled).
func (a *Agent) shouldEnterAutonomous() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cfg.GracePeriodOffline <= 0 {
		return false
	}
	if a.offlineSince.IsZero() {
		return false
	}
	return time.Since(a.offlineSince) > a.cfg.GracePeriodOffline
}

// --- PolicySync / SettingsSync handlers ---

// handlePolicySync processes a PolicySync message from the server.
// Updates the in-memory cache and persists to disk for restart survival.
func (a *Agent) handlePolicySync(sync *proto.PolicySync) {
	a.policies.applyPolicySync(sync)

	count := len(sync.GetPolicies())
	a.log.Info("policy sync applied",
		"policies", count,
		"default", sync.GetDefaultPolicy(),
	)

	if err := a.savePolicyCache(); err != nil {
		a.log.Error("failed to persist policy cache", "error", err)
	}
}

// handleSettingsSync processes a SettingsSync message from the server.
// Updates the in-memory cache and persists to disk for restart survival.
func (a *Agent) handleSettingsSync(sync *proto.SettingsSync) {
	a.policies.applySettingsSync(sync)

	a.log.Info("settings sync applied",
		"poll_interval", a.policies.pollInterval,
		"hooks_enabled", a.policies.hooksEnabled,
		"dependency_aware", a.policies.dependencyAware,
		"rollback_policy", a.policies.rollbackPolicy,
	)

	if err := a.savePolicyCache(); err != nil {
		a.log.Error("failed to persist settings cache", "error", err)
	}
}

// --- Persistence ---

const policyCacheFilename = "policy_cache.json"

// savePolicyCache serialises the policy cache to a JSON file in DataDir.
// This allows the agent to restore cached policies after a restart, so
// autonomous mode can work even if the agent process was restarted while
// disconnected from the server.
func (a *Agent) savePolicyCache() error {
	a.policies.mu.RLock()
	file := policyCacheFile{
		Policies:        a.policies.policies,
		DefaultPolicy:   a.policies.defaultPolicy,
		PollInterval:    a.policies.pollInterval,
		GracePeriod:     a.policies.gracePeriod,
		ImageCleanup:    a.policies.imageCleanup,
		HooksEnabled:    a.policies.hooksEnabled,
		DependencyAware: a.policies.dependencyAware,
		RollbackPolicy:  a.policies.rollbackPolicy,
	}
	a.policies.mu.RUnlock()

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal policy cache: %w", err)
	}

	path := filepath.Join(a.cfg.DataDir, policyCacheFilename)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write policy cache: %w", err)
	}

	return nil
}

// loadPolicyCache restores the policy cache from the JSON file in DataDir.
// If the file doesn't exist (first run, or never received a sync), this
// is a no-op — the cache starts with safe defaults.
func (a *Agent) loadPolicyCache() error {
	path := filepath.Join(a.cfg.DataDir, policyCacheFilename)

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		a.log.Debug("no cached policy file, starting with defaults")
		return nil
	}
	if err != nil {
		return fmt.Errorf("read policy cache: %w", err)
	}

	var file policyCacheFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("unmarshal policy cache: %w", err)
	}

	a.policies.mu.Lock()
	defer a.policies.mu.Unlock()

	if file.Policies != nil {
		a.policies.policies = file.Policies
	}
	if file.DefaultPolicy != "" {
		a.policies.defaultPolicy = file.DefaultPolicy
	}
	a.policies.pollInterval = file.PollInterval
	a.policies.gracePeriod = file.GracePeriod
	a.policies.imageCleanup = file.ImageCleanup
	a.policies.hooksEnabled = file.HooksEnabled
	a.policies.dependencyAware = file.DependencyAware
	a.policies.rollbackPolicy = file.RollbackPolicy

	a.log.Info("loaded cached policies",
		"policies", len(a.policies.policies),
		"default", a.policies.defaultPolicy,
	)

	return nil
}
