package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsRegistered(t *testing.T) {
	// Initialise CounterVec label combinations so they appear in Gather output.
	// CounterVec metrics are not gathered until at least one label set is created.
	UpdatesTotal.WithLabelValues("success")
	RegistryErrors.WithLabelValues("docker.io")

	// Verify all metrics are registered by gathering them.
	// promauto registers on init, so if we get here without panic, registration succeeded.
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	expected := map[string]bool{
		"sentinel_containers_total":        false,
		"sentinel_containers_monitored":    false,
		"sentinel_updates_total":           false,
		"sentinel_update_duration_seconds": false,
		"sentinel_scan_duration_seconds":   false,
		"sentinel_scans_total":             false,
		"sentinel_pending_updates":         false,
		"sentinel_queued_updates":          false,
		"sentinel_image_cleanups_total":    false,
		"sentinel_registry_errors_total":   false,
	}

	for _, mf := range mfs {
		if _, ok := expected[mf.GetName()]; ok {
			expected[mf.GetName()] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("metric %q not registered", name)
		}
	}
}

func TestCounterIncrements(t *testing.T) {
	ScansTotal.Add(1)
	ImageCleanups.Add(1)
	UpdatesTotal.WithLabelValues("success").Inc()
	UpdatesTotal.WithLabelValues("failed").Inc()
	// No panic = success; actual values verified via Gather if needed.
}

func TestGaugeSets(t *testing.T) {
	ContainersTotal.Set(10)
	ContainersMonitored.Set(8)
	PendingUpdates.Set(3)
	QueuedUpdates.Set(2)
	// No panic = success.
}
