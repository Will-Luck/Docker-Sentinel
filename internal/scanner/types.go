package scanner

import "time"

// Severity levels for vulnerabilities.
type Severity string

const (
	SeverityCritical Severity = "CRITICAL"
	SeverityHigh     Severity = "HIGH"
	SeverityMedium   Severity = "MEDIUM"
	SeverityLow      Severity = "LOW"
	SeverityUnknown  Severity = "UNKNOWN"
)

// SeverityPriority returns the numeric priority (higher = more severe).
func SeverityPriority(s Severity) int {
	switch s {
	case SeverityCritical:
		return 4
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 1
	default:
		return 0
	}
}

// ParseSeverity converts a string to a Severity.
func ParseSeverity(s string) Severity {
	switch Severity(s) {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
		return Severity(s)
	default:
		return SeverityUnknown
	}
}

// ScanMode determines when scanning happens.
type ScanMode string

const (
	ScanDisabled   ScanMode = "disabled"    // no scanning (default)
	ScanPreUpdate  ScanMode = "pre-update"  // scan before replacing container
	ScanPostUpdate ScanMode = "post-update" // scan after update (informational)
)

// ParseScanMode converts a string to a ScanMode.
func ParseScanMode(s string) ScanMode {
	switch ScanMode(s) {
	case ScanPreUpdate, ScanPostUpdate:
		return ScanMode(s)
	default:
		return ScanDisabled
	}
}

// Vulnerability represents a single CVE found by scanning.
type Vulnerability struct {
	ID           string   `json:"id"` // e.g. "CVE-2024-1234"
	Severity     Severity `json:"severity"`
	Title        string   `json:"title"`
	Description  string   `json:"description,omitempty"`
	PkgName      string   `json:"pkg_name"`
	InstalledVer string   `json:"installed_version"`
	FixedVer     string   `json:"fixed_version,omitempty"`
}

// ScanResult holds the aggregated results from a Trivy scan.
type ScanResult struct {
	ImageRef  string          `json:"image_ref"`
	ScannedAt time.Time       `json:"scanned_at"`
	Summary   Summary         `json:"summary"`
	Vulns     []Vulnerability `json:"vulnerabilities,omitempty"`
}

// Summary provides vulnerability counts by severity.
type Summary struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
	Total    int `json:"total"`
}

// ExceedsThreshold returns true if the result has vulnerabilities at or above the threshold.
func (r *ScanResult) ExceedsThreshold(threshold Severity) bool {
	threshPri := SeverityPriority(threshold)
	for _, v := range r.Vulns {
		if SeverityPriority(v.Severity) >= threshPri {
			return true
		}
	}
	return false
}
