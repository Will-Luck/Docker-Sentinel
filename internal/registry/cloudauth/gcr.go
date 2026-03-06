package cloudauth

import (
	"context"
	"strings"
	"time"
)

// GCRConfig holds Google GCR/Artifact Registry authentication configuration.
type GCRConfig struct {
	ServiceAccountJSON string `json:"service_account_json"` // full JSON key file content
}

type gcrProvider struct {
	cfg GCRConfig
}

func NewGCR(cfg GCRConfig) Provider {
	return &gcrProvider{cfg: cfg}
}

func (p *gcrProvider) Name() string { return "gcr" }

func (p *gcrProvider) Matches(host string) bool {
	// GCR: gcr.io, us.gcr.io, eu.gcr.io, asia.gcr.io
	// Artifact Registry: *-docker.pkg.dev
	if host == "gcr.io" || strings.HasSuffix(host, ".gcr.io") {
		return true
	}
	if strings.HasSuffix(host, "-docker.pkg.dev") {
		return true
	}
	return false
}

func (p *gcrProvider) GetCredentials(_ context.Context) (string, string, time.Time, error) {
	// GCR and Artifact Registry accept Basic auth with:
	//   username: _json_key
	//   password: <service account JSON content>
	// This doesn't expire per se, but we set a 1-hour cache window.
	expiry := time.Now().Add(1 * time.Hour)
	return "_json_key", p.cfg.ServiceAccountJSON, expiry, nil
}
