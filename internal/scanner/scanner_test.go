package scanner

import (
	"testing"
	"time"
)

type testLogger struct{}

func (l *testLogger) Info(msg string, args ...any)  {}
func (l *testLogger) Error(msg string, args ...any) {}

func TestParseSeverity(t *testing.T) {
	tests := []struct {
		input string
		want  Severity
	}{
		{"CRITICAL", SeverityCritical},
		{"HIGH", SeverityHigh},
		{"MEDIUM", SeverityMedium},
		{"LOW", SeverityLow},
		{"UNKNOWN", SeverityUnknown},
		{"invalid", SeverityUnknown},
		{"", SeverityUnknown},
	}
	for _, tt := range tests {
		if got := ParseSeverity(tt.input); got != tt.want {
			t.Errorf("ParseSeverity(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSeverityPriority(t *testing.T) {
	if SeverityPriority(SeverityCritical) <= SeverityPriority(SeverityHigh) {
		t.Error("CRITICAL should be higher than HIGH")
	}
	if SeverityPriority(SeverityHigh) <= SeverityPriority(SeverityMedium) {
		t.Error("HIGH should be higher than MEDIUM")
	}
	if SeverityPriority(SeverityMedium) <= SeverityPriority(SeverityLow) {
		t.Error("MEDIUM should be higher than LOW")
	}
}

func TestParseScanMode(t *testing.T) {
	tests := []struct {
		input string
		want  ScanMode
	}{
		{"pre-update", ScanPreUpdate},
		{"post-update", ScanPostUpdate},
		{"disabled", ScanDisabled},
		{"invalid", ScanDisabled},
		{"", ScanDisabled},
	}
	for _, tt := range tests {
		if got := ParseScanMode(tt.input); got != tt.want {
			t.Errorf("ParseScanMode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseTrivyOutput(t *testing.T) {
	trivyJSON := `{
		"Results": [
			{
				"Target": "nginx:1.25 (debian 12.5)",
				"Vulnerabilities": [
					{
						"VulnerabilityID": "CVE-2024-1234",
						"PkgName": "libssl3",
						"InstalledVersion": "3.0.11-1",
						"FixedVersion": "3.0.13-1",
						"Severity": "CRITICAL",
						"Title": "OpenSSL: Buffer overflow",
						"Description": "A buffer overflow in libssl"
					},
					{
						"VulnerabilityID": "CVE-2024-5678",
						"PkgName": "curl",
						"InstalledVersion": "7.88.1-10",
						"FixedVersion": "7.88.1-11",
						"Severity": "HIGH",
						"Title": "curl: Use after free",
						"Description": "Use after free in curl"
					},
					{
						"VulnerabilityID": "CVE-2024-9012",
						"PkgName": "zlib1g",
						"InstalledVersion": "1.2.13",
						"FixedVersion": "",
						"Severity": "LOW",
						"Title": "zlib: Minor issue",
						"Description": "A minor issue"
					}
				]
			}
		]
	}`

	result, err := parseTrivyOutput("nginx:1.25", []byte(trivyJSON))
	if err != nil {
		t.Fatalf("parseTrivyOutput: %v", err)
	}

	if result.ImageRef != "nginx:1.25" {
		t.Errorf("ImageRef = %q, want nginx:1.25", result.ImageRef)
	}
	if result.Summary.Critical != 1 {
		t.Errorf("Critical = %d, want 1", result.Summary.Critical)
	}
	if result.Summary.High != 1 {
		t.Errorf("High = %d, want 1", result.Summary.High)
	}
	if result.Summary.Low != 1 {
		t.Errorf("Low = %d, want 1", result.Summary.Low)
	}
	if result.Summary.Total != 3 {
		t.Errorf("Total = %d, want 3", result.Summary.Total)
	}
	if len(result.Vulns) != 3 {
		t.Fatalf("got %d vulns, want 3", len(result.Vulns))
	}

	// Verify first vuln details.
	v := result.Vulns[0]
	if v.ID != "CVE-2024-1234" {
		t.Errorf("Vuln[0].ID = %q", v.ID)
	}
	if v.Severity != SeverityCritical {
		t.Errorf("Vuln[0].Severity = %q", v.Severity)
	}
	if v.PkgName != "libssl3" {
		t.Errorf("Vuln[0].PkgName = %q", v.PkgName)
	}
}

func TestParseTrivyOutputEmpty(t *testing.T) {
	result, err := parseTrivyOutput("nginx:latest", []byte(`{"Results": []}`))
	if err != nil {
		t.Fatalf("parseTrivyOutput: %v", err)
	}
	if result.Summary.Total != 0 {
		t.Errorf("Total = %d, want 0", result.Summary.Total)
	}
}

func TestParseTrivyOutputNoVulns(t *testing.T) {
	result, err := parseTrivyOutput("alpine:latest", []byte(`{"Results": [{"Target": "alpine", "Vulnerabilities": null}]}`))
	if err != nil {
		t.Fatalf("parseTrivyOutput: %v", err)
	}
	if result.Summary.Total != 0 {
		t.Errorf("Total = %d, want 0", result.Summary.Total)
	}
}

func TestParseTrivyOutputInvalid(t *testing.T) {
	_, err := parseTrivyOutput("nginx", []byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestExceedsThreshold(t *testing.T) {
	result := &ScanResult{
		Vulns: []Vulnerability{
			{ID: "CVE-1", Severity: SeverityHigh},
			{ID: "CVE-2", Severity: SeverityLow},
		},
	}

	if !result.ExceedsThreshold(SeverityHigh) {
		t.Error("should exceed HIGH threshold")
	}
	if !result.ExceedsThreshold(SeverityLow) {
		t.Error("should exceed LOW threshold")
	}
	if result.ExceedsThreshold(SeverityCritical) {
		t.Error("should NOT exceed CRITICAL threshold (only has HIGH)")
	}
}

func TestExceedsThresholdEmpty(t *testing.T) {
	result := &ScanResult{}
	if result.ExceedsThreshold(SeverityLow) {
		t.Error("empty result should not exceed any threshold")
	}
}

func TestScanResultTimestamp(t *testing.T) {
	result, err := parseTrivyOutput("test", []byte(`{"Results": []}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.ScannedAt.IsZero() {
		t.Error("ScannedAt should be set")
	}
	if time.Since(result.ScannedAt) > 5*time.Second {
		t.Error("ScannedAt should be recent")
	}
}

func TestAvailableNonExistent(t *testing.T) {
	s := New(&testLogger{}, WithTrivyPath("/nonexistent/trivy"))
	if s.Available() {
		t.Error("expected not available")
	}
}
