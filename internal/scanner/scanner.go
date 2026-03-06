package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Logger is a minimal logging interface.
type Logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

// Scanner wraps the Trivy CLI to scan container images.
type Scanner struct {
	trivyPath  string // path to trivy binary (default: "trivy")
	severities string // comma-separated severity filter (e.g. "CRITICAL,HIGH")
	log        Logger
}

// Option configures a Scanner.
type Option func(*Scanner)

// WithTrivyPath sets the path to the trivy binary.
func WithTrivyPath(path string) Option {
	return func(s *Scanner) { s.trivyPath = path }
}

// WithSeverities sets the severity filter.
func WithSeverities(sev string) Option {
	return func(s *Scanner) { s.severities = sev }
}

// New creates a Scanner with the given options.
func New(log Logger, opts ...Option) *Scanner {
	s := &Scanner{
		trivyPath:  "trivy",
		severities: "CRITICAL,HIGH,MEDIUM,LOW",
		log:        log,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Available checks whether the trivy binary is installed and reachable.
func (s *Scanner) Available() bool {
	_, err := exec.LookPath(s.trivyPath)
	return err == nil
}

// Scan runs Trivy against the given image reference and returns parsed results.
func (s *Scanner) Scan(ctx context.Context, imageRef string) (*ScanResult, error) {
	if imageRef == "" {
		return nil, fmt.Errorf("empty image reference")
	}

	args := []string{
		"image",
		"--format", "json",
		"--severity", s.severities,
		"--quiet",
		imageRef,
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, s.trivyPath, args...) //nolint:gosec // trivyPath is operator-configured, not user input
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Trivy exits non-zero on scan failures (not just on vulns found).
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return nil, fmt.Errorf("trivy scan: %s", errMsg)
	}

	return parseTrivyOutput(imageRef, stdout.Bytes())
}

// parseTrivyOutput converts Trivy JSON output into a ScanResult.
func parseTrivyOutput(imageRef string, data []byte) (*ScanResult, error) {
	// Trivy JSON format has a top-level "Results" array, each with
	// a "Vulnerabilities" array.
	var report trivyReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("parse trivy output: %w", err)
	}

	result := &ScanResult{
		ImageRef:  imageRef,
		ScannedAt: time.Now().UTC(),
	}

	for _, target := range report.Results {
		for _, vuln := range target.Vulnerabilities {
			sev := ParseSeverity(vuln.Severity)
			result.Vulns = append(result.Vulns, Vulnerability{
				ID:           vuln.VulnerabilityID,
				Severity:     sev,
				Title:        vuln.Title,
				Description:  vuln.Description,
				PkgName:      vuln.PkgName,
				InstalledVer: vuln.InstalledVersion,
				FixedVer:     vuln.FixedVersion,
			})

			switch sev {
			case SeverityCritical:
				result.Summary.Critical++
			case SeverityHigh:
				result.Summary.High++
			case SeverityMedium:
				result.Summary.Medium++
			case SeverityLow:
				result.Summary.Low++
			default:
				result.Summary.Unknown++
			}
			result.Summary.Total++
		}
	}

	return result, nil
}

// trivyReport maps the Trivy JSON output structure.
type trivyReport struct {
	Results []trivyTarget `json:"Results"`
}

type trivyTarget struct {
	Target          string      `json:"Target"`
	Vulnerabilities []trivyVuln `json:"Vulnerabilities"`
}

type trivyVuln struct {
	VulnerabilityID  string `json:"VulnerabilityID"`
	PkgName          string `json:"PkgName"`
	InstalledVersion string `json:"InstalledVersion"`
	FixedVersion     string `json:"FixedVersion"`
	Severity         string `json:"Severity"`
	Title            string `json:"Title"`
	Description      string `json:"Description"`
}
