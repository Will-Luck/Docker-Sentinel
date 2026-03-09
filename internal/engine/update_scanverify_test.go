package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/scanner"
	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
	"github.com/moby/moby/api/types/container"
)

// mockImageScanner implements ImageScanner for testing.
type mockImageScanner struct {
	result *scanner.ScanResult
	err    error
	calls  []string
}

func (m *mockImageScanner) Scan(_ context.Context, imageRef string) (*scanner.ScanResult, error) {
	m.calls = append(m.calls, imageRef)
	return m.result, m.err
}

// mockImageVerifier implements ImageVerifier for testing.
type mockImageVerifier struct {
	result *verify.Result
	calls  []string
}

func (m *mockImageVerifier) Verify(_ context.Context, imageRef string) *verify.Result {
	m.calls = append(m.calls, imageRef)
	return m.result
}

// setupUpdateWithScanVerify creates a standard mock and updater with scanner/verifier wired in.
// The container "aaa" is the original; "new-nginx" is created during the update.
func setupUpdateWithScanVerify(t *testing.T) (*mockDocker, *Updater) {
	t.Helper()
	mock := newMockDocker()

	mock.inspectResults["aaa"] = container.InspectResponse{
		ID:    "aaa",
		Name:  "/nginx",
		Image: "sha256:old111",
		Config: &container.Config{
			Image:  "docker.io/library/nginx:latest",
			Labels: map[string]string{},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	// Different image IDs so the update proceeds past the image ID guard.
	mock.imageIDs["docker.io/library/nginx:latest"] = "sha256:new222"

	// New container passes validation.
	mock.inspectResults["new-nginx"] = container.InspectResponse{
		ID:   "new-nginx",
		Name: "/nginx",
		State: &container.State{
			Running:    true,
			Restarting: false,
		},
		Config: &container.Config{
			Image:  "docker.io/library/nginx:latest",
			Labels: map[string]string{"sentinel.maintenance": "true"},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	u, _ := newTestUpdater(t, mock)
	return mock, u
}

func TestUpdateContainer_PreScanBlocks(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)

	ms := &mockImageScanner{
		result: &scanner.ScanResult{
			ImageRef:  "docker.io/library/nginx:latest",
			ScannedAt: time.Now(),
			Summary:   scanner.Summary{Critical: 2, Total: 2},
			Vulns: []scanner.Vulnerability{
				{ID: "CVE-2024-0001", Severity: scanner.SeverityCritical},
				{ID: "CVE-2024-0002", Severity: scanner.SeverityCritical},
			},
		},
	}

	u.SetScanner(ms)
	u.SetScanMode(scanner.ScanPreUpdate)
	u.SetSeverityThreshold(scanner.SeverityHigh)

	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")
	if err == nil {
		t.Fatal("expected error from pre-scan blocking update")
	}

	// Pull should have been called (scan happens after pull).
	if len(mock.pullCalls) != 1 {
		t.Errorf("pullCalls = %d, want 1", len(mock.pullCalls))
	}

	// Scanner should have been called.
	if len(ms.calls) != 1 {
		t.Errorf("scanner calls = %d, want 1", len(ms.calls))
	}

	// Destructive operations should NOT have been called (update blocked).
	if len(mock.stopCalls) != 0 {
		t.Errorf("stopCalls = %d, want 0 (scan should block)", len(mock.stopCalls))
	}
	if len(mock.createCalls) != 0 {
		t.Errorf("createCalls = %d, want 0 (scan should block)", len(mock.createCalls))
	}
}

func TestUpdateContainer_PreScanPasses(t *testing.T) {
	_, u := setupUpdateWithScanVerify(t)

	ms := &mockImageScanner{
		result: &scanner.ScanResult{
			ImageRef:  "docker.io/library/nginx:latest",
			ScannedAt: time.Now(),
			Summary:   scanner.Summary{Low: 3, Total: 3},
			Vulns: []scanner.Vulnerability{
				{ID: "CVE-2024-1001", Severity: scanner.SeverityLow},
				{ID: "CVE-2024-1002", Severity: scanner.SeverityLow},
				{ID: "CVE-2024-1003", Severity: scanner.SeverityLow},
			},
		},
	}

	u.SetScanner(ms)
	u.SetScanMode(scanner.ScanPreUpdate)
	u.SetSeverityThreshold(scanner.SeverityHigh) // threshold is HIGH, vulns are LOW

	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	// Scanner should have been called.
	if len(ms.calls) != 1 {
		t.Errorf("scanner calls = %d, want 1", len(ms.calls))
	}
}

func TestUpdateContainer_VerifyEnforceBlocks(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)

	mv := &mockImageVerifier{
		result: &verify.Result{
			Verified: false,
			Error:    "no matching signatures",
		},
	}

	u.SetVerifier(mv)
	u.SetVerifyMode(verify.ModeEnforce)

	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")
	if err == nil {
		t.Fatal("expected error from enforce verification failure")
	}

	// Pull should have been called (verify happens after pull).
	if len(mock.pullCalls) != 1 {
		t.Errorf("pullCalls = %d, want 1", len(mock.pullCalls))
	}

	// Verifier should have been called.
	if len(mv.calls) != 1 {
		t.Errorf("verifier calls = %d, want 1", len(mv.calls))
	}

	// Destructive operations should NOT have been called.
	if len(mock.stopCalls) != 0 {
		t.Errorf("stopCalls = %d, want 0 (verify enforce should block)", len(mock.stopCalls))
	}
}

func TestUpdateContainer_VerifyWarnProceeds(t *testing.T) {
	_, u := setupUpdateWithScanVerify(t)

	mv := &mockImageVerifier{
		result: &verify.Result{
			Verified: false,
			Error:    "no matching signatures",
		},
	}

	u.SetVerifier(mv)
	u.SetVerifyMode(verify.ModeWarn)

	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")
	if err != nil {
		t.Fatalf("expected nil error (warn mode should proceed), got: %v", err)
	}

	// Verifier should have been called.
	if len(mv.calls) != 1 {
		t.Errorf("verifier calls = %d, want 1", len(mv.calls))
	}
}

func TestUpdateContainer_ScannerError(t *testing.T) {
	_, u := setupUpdateWithScanVerify(t)

	ms := &mockImageScanner{
		err: fmt.Errorf("trivy binary not found"),
	}

	u.SetScanner(ms)
	u.SetScanMode(scanner.ScanPreUpdate)
	u.SetSeverityThreshold(scanner.SeverityCritical)

	// Scanner error should not block the update — tool failures are non-fatal.
	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")
	if err != nil {
		t.Fatalf("expected nil error (scanner errors should not block), got: %v", err)
	}

	// Scanner should have been called.
	if len(ms.calls) != 1 {
		t.Errorf("scanner calls = %d, want 1", len(ms.calls))
	}
}
