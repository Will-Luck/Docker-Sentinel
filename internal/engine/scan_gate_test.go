package engine

import (
	"context"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/scanner"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

func criticalScan() *mockImageScanner {
	return &mockImageScanner{result: &scanner.ScanResult{
		ImageRef: "docker.io/library/nginx:latest", ScannedAt: time.Now(),
		Summary: scanner.Summary{Critical: 1, Total: 1},
		Vulns:   []scanner.Vulnerability{{ID: "CVE-2024-0001", Severity: scanner.SeverityCritical}},
	}}
}

// Scanner settings saved in the web UI apply to the next update, without a
// restart: the mode, threshold and Trivy path are read when the update runs.
func TestPreUpdateScanGateFollowsSavedSettings(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	ms := criticalScan()
	var gotPath string
	u.SetScannerFactory(func(path string) ImageScanner {
		gotPath = path
		return ms
	})
	u.SetSettingsReader(&testSettings{data: map[string]string{
		store.SettingScannerMode:      "pre-update",
		store.SettingScannerThreshold: "HIGH",
		store.SettingTrivyPath:        "/opt/trivy",
	}})

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err == nil {
		t.Fatal("a critical vulnerability did not block the update")
	}
	if gotPath != "/opt/trivy" || len(ms.calls) != 1 {
		t.Errorf("trivy path %q, scans %d; want the saved path and one scan", gotPath, len(ms.calls))
	}
	if len(mock.createCalls) != 0 {
		t.Errorf("the container was replaced despite the block: %v", mock.createCalls)
	}
}

func TestPreUpdateScanGateIsOffWhenSettingSaysDisabled(t *testing.T) {
	_, u := setupUpdateWithScanVerify(t)
	ms := criticalScan()
	u.SetScannerFactory(func(string) ImageScanner { return ms })
	u.SetSettingsReader(&testSettings{data: map[string]string{store.SettingScannerMode: "disabled"}})

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("scanning is disabled but the update failed: %v", err)
	}
	if len(ms.calls) != 0 {
		t.Error("scanned although disabled")
	}
}

// With the mode set to pre-update but no Trivy to run, the update goes ahead
// and says so at warning level, instead of skipping the check silently.
func TestPreUpdateScanGateWithoutTrivyProceedsAndReports(t *testing.T) {
	_, u := setupUpdateWithScanVerify(t)
	u.SetScannerFactory(func(string) ImageScanner { return nil })
	u.SetSettingsReader(&testSettings{data: map[string]string{store.SettingScannerMode: "pre-update"}})

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if u.ScannerAvailable() {
		t.Error("ScannerAvailable() = true with no Trivy")
	}
}
