package engine

import (
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/scanner"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// ScannerFactory builds the vulnerability scanner for a Trivy path, or returns
// nil when Trivy is not available there.
type ScannerFactory func(trivyPath string) ImageScanner

// scannerRecheck is how long an unavailable scanner is trusted to stay
// unavailable before the path is probed again (Trivy may have been installed).
const scannerRecheck = time.Minute

type scannerCache struct {
	mu        sync.Mutex
	path      string
	scanner   ImageScanner
	checkedAt time.Time
}

// SetScannerFactory sets how the scanner is built from the saved Trivy path.
// With a factory the scanner follows the saved settings; without one the
// scanner given to SetScanner is used.
func (u *Updater) SetScannerFactory(f ScannerFactory) {
	u.scannerFactory = f
}

// scanGate returns the vulnerability scanner, scan mode and severity threshold
// to apply to an update now. The saved settings win over what was set at
// startup, so the web UI's scanner settings apply without a restart.
func (u *Updater) scanGate() (ImageScanner, scanner.ScanMode, scanner.Severity) {
	mode, thresh, path := u.scanMode, u.severityThresh, ""
	if u.settings != nil {
		if v, err := u.settings.LoadSetting(store.SettingScannerMode); err == nil && v != "" {
			mode = scanner.ParseScanMode(v)
		}
		if v, err := u.settings.LoadSetting(store.SettingScannerThreshold); err == nil && v != "" {
			thresh = scanner.Severity(v)
		}
		if v, err := u.settings.LoadSetting(store.SettingTrivyPath); err == nil {
			path = v
		}
	}
	if thresh == "" {
		thresh = scanner.SeverityHigh
	}
	if u.scannerFactory == nil {
		return u.imgScanner, mode, thresh
	}
	if path == "" {
		path = "trivy"
	}

	c := &u.scanCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path != path || (c.scanner == nil && time.Since(c.checkedAt) >= scannerRecheck) || c.checkedAt.IsZero() {
		c.path, c.scanner, c.checkedAt = path, u.scannerFactory(path), time.Now()
	}
	return c.scanner, mode, thresh
}

// ScannerAvailable reports whether a vulnerability scanner can run now, for the
// settings page to warn when scanning is chosen but Trivy is missing.
func (u *Updater) ScannerAvailable() bool {
	s, _, _ := u.scanGate()
	return s != nil
}
