package guardian

import "testing"

func TestMaintenanceLabelConstant(t *testing.T) {
	if MaintenanceLabel != "sentinel.maintenance" {
		t.Errorf("MaintenanceLabel = %q, want sentinel.maintenance", MaintenanceLabel)
	}
}

func TestHasMaintenanceLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"label set true", map[string]string{"sentinel.maintenance": "true"}, true},
		{"label set false", map[string]string{"sentinel.maintenance": "false"}, false},
		{"label missing", map[string]string{}, false},
		{"nil labels", nil, false},
		{"other labels only", map[string]string{"foo": "bar"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasMaintenanceLabel(tt.labels); got != tt.want {
				t.Errorf("HasMaintenanceLabel(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}
