package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	ContainersTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "sentinel_containers_total",
		Help: "Total number of containers on the host.",
	})
	ContainersMonitored = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "sentinel_containers_monitored",
		Help: "Number of containers being monitored for updates.",
	})
	UpdatesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sentinel_updates_total",
		Help: "Total number of container updates by status.",
	}, []string{"status"})
	UpdateDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "sentinel_update_duration_seconds",
		Help:    "Duration of container update operations.",
		Buckets: prometheus.DefBuckets,
	})
	ScanDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "sentinel_scan_duration_seconds",
		Help:    "Duration of update scan operations.",
		Buckets: prometheus.DefBuckets,
	})
	ScansTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sentinel_scans_total",
		Help: "Total number of update scans performed.",
	})
	PendingUpdates = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "sentinel_pending_updates",
		Help: "Number of containers with available updates.",
	})
	QueuedUpdates = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "sentinel_queued_updates",
		Help: "Number of updates waiting in the approval queue.",
	})
	ImageCleanups = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sentinel_image_cleanups_total",
		Help: "Total number of old images cleaned up after updates.",
	})
	RegistryErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sentinel_registry_errors_total",
		Help: "Total number of registry check errors by registry.",
	}, []string{"registry"})
)
