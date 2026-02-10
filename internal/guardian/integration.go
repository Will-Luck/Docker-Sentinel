// Package guardian provides integration helpers for Docker-Guardian compatibility.
package guardian

// MaintenanceLabel is the label key that Sentinel sets during updates.
// Guardian skips containers with this label set to "true".
const MaintenanceLabel = "sentinel.maintenance"

// HasMaintenanceLabel returns true if the container has the sentinel.maintenance
// label set to "true".
func HasMaintenanceLabel(labels map[string]string) bool {
	return labels[MaintenanceLabel] == "true"
}
