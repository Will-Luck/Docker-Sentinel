package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/web"
)

// SENTINEL_CLUSTER_PORT was ignored: the listener took only the saved
// setting (the cluster settings form saves its port with every change) or
// 9443. Set in the environment, it wins, as SENTINEL_CLUSTER_ENABLED does.
func TestClusterListenPort(t *testing.T) {
	for _, tc := range []struct{ env, saved, want string }{
		{"9555", "9443", "9555"},
		{"", "9600", "9600"},
		{"", "", "9443"},
	} {
		if got := clusterListenPort(tc.env, tc.saved); got != tc.want {
			t.Errorf("env %q, saved %q: port %q, want %q", tc.env, tc.saved, got, tc.want)
		}
	}
}

// An agent with SENTINEL_WEB_ENABLED=false serves no web UI.
func TestStartAgentWeb_OffWhenWebDisabled(t *testing.T) {
	cfg := config.NewTestConfig()
	cfg.WebEnabled = false
	cfg.WebPort = "0"
	if srv := startAgentWeb(cfg, web.AgentDeps{}, logging.New(false)); srv != nil {
		t.Error("the agent web UI was started although SENTINEL_WEB_ENABLED is false")
	}
}

// Stopping an agent is not an error: Run returns the cancelled context.
func TestAgentExitErr(t *testing.T) {
	if err := agentExitErr(context.Canceled); err != nil {
		t.Errorf("graceful stop reported as %v", err)
	}
	if err := agentExitErr(fmt.Errorf("session: %w", context.Canceled)); err != nil {
		t.Errorf("wrapped graceful stop reported as %v", err)
	}
	if err := agentExitErr(errors.New("enrollment failed")); err == nil {
		t.Error("a real failure was swallowed")
	}
}

// SENTINEL_GRACE_PERIOD on an agent pins its grace period.
func TestAgentEnvSettings_GracePeriod(t *testing.T) {
	got := agentEnvSettings(func(k string) (string, bool) { return "1m", k == "SENTINEL_GRACE_PERIOD" })
	if !got["grace_period"] {
		t.Errorf("env settings = %v, want grace_period", got)
	}
}
