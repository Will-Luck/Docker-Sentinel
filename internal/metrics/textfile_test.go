package metrics

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestWriteTextfileFormat pins the exposition format of the textfile that
// node_exporter's textfile collector reads, so a prometheus/common or
// client_golang bump that changes it fails here instead of in someone's
// dashboard.
func TestWriteTextfileFormat(t *testing.T) {
	// Values set here are exact; the label values are unique to this test so
	// counters other tests touch cannot change the pinned lines.
	ContainersTotal.Set(12)
	ContainersMonitored.Set(9)
	PendingUpdates.Set(4)
	QueuedUpdates.Set(2)
	UpdatesTotal.WithLabelValues("format_pin").Inc()
	RegistryErrors.WithLabelValues("registry.example.com").Add(3)
	ScansTotal.Inc()
	ImageCleanups.Inc()
	UpdateDuration.Observe(0.5)
	ScanDuration.Observe(0.5)

	path := filepath.Join(t.TempDir(), "sentinel.prom")
	if err := WriteTextfile(path); err != nil {
		t.Fatalf("WriteTextfile: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)

	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("the temporary file was left behind (stat error: %v)", err)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("output does not end with a newline")
	}
	if strings.Contains(out, "# EOF") {
		t.Error("output carries the OpenMetrics terminator; the textfile collector reads the classic text format")
	}

	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	have := make(map[string]bool, len(lines))
	for _, l := range lines {
		have[l] = true
		if l == "" {
			t.Error("output contains a blank line")
		}
		if strings.HasPrefix(l, "# ") {
			continue
		}
		if !strings.HasPrefix(l, "sentinel_") {
			t.Errorf("non-sentinel sample in the textfile: %q", l)
		}
	}

	exact := []string{
		// Plain metrics: HELP, TYPE and the sample, in that form.
		"# HELP sentinel_containers_total Total number of containers on the host.",
		"# TYPE sentinel_containers_total gauge",
		"sentinel_containers_total 12",
		"# HELP sentinel_containers_monitored Number of containers being monitored for updates.",
		"# TYPE sentinel_containers_monitored gauge",
		"sentinel_containers_monitored 9",
		"# HELP sentinel_pending_updates Number of containers with available updates.",
		"# TYPE sentinel_pending_updates gauge",
		"sentinel_pending_updates 4",
		"# HELP sentinel_queued_updates Number of updates waiting in the approval queue.",
		"# TYPE sentinel_queued_updates gauge",
		"sentinel_queued_updates 2",
		"# HELP sentinel_scans_total Total number of update scans performed.",
		"# TYPE sentinel_scans_total counter",
		"# HELP sentinel_image_cleanups_total Total number of old images cleaned up after updates.",
		"# TYPE sentinel_image_cleanups_total counter",
		// Labelled counters: quoted label values, no spaces inside the braces.
		"# HELP sentinel_updates_total Total number of container updates by status.",
		"# TYPE sentinel_updates_total counter",
		`sentinel_updates_total{status="format_pin"} 1`,
		"# HELP sentinel_registry_errors_total Total number of registry check errors by registry.",
		"# TYPE sentinel_registry_errors_total counter",
		`sentinel_registry_errors_total{registry="registry.example.com"} 3`,
		// Histograms.
		"# HELP sentinel_update_duration_seconds Duration of container update operations.",
		"# TYPE sentinel_update_duration_seconds histogram",
		"# HELP sentinel_scan_duration_seconds Duration of update scan operations.",
		"# TYPE sentinel_scan_duration_seconds histogram",
		`sentinel_update_duration_seconds_bucket{le="0.005"} 0`,
		`sentinel_update_duration_seconds_bucket{le="0.5"} `,
		`sentinel_scan_duration_seconds_bucket{le="10"} `,
	}
	for _, want := range exact {
		if strings.HasSuffix(want, " ") {
			if !hasLinePrefix(lines, want) {
				t.Errorf("no line starts with %q", want)
			}
			continue
		}
		if !have[want] {
			t.Errorf("missing line %q", want)
		}
	}

	// Values that other tests move, so only their shape is pinned.
	shapes := []string{
		`^sentinel_scans_total [0-9]+$`,
		`^sentinel_image_cleanups_total [0-9]+$`,
		`^sentinel_update_duration_seconds_bucket\{le="\+Inf"\} [0-9]+$`,
		`^sentinel_update_duration_seconds_sum [0-9.e+-]+$`,
		`^sentinel_update_duration_seconds_count [0-9]+$`,
		`^sentinel_scan_duration_seconds_bucket\{le="\+Inf"\} [0-9]+$`,
		`^sentinel_scan_duration_seconds_sum [0-9.e+-]+$`,
		`^sentinel_scan_duration_seconds_count [0-9]+$`,
	}
	for _, s := range shapes {
		re := regexp.MustCompile(s)
		found := false
		for _, l := range lines {
			if re.MatchString(l) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no line matches %s", s)
		}
	}
}

func hasLinePrefix(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}
