package main

import (
	"encoding/json"

	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry/cloudauth"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// buildCloudAuth registers the cloud registry providers (AWS ECR, Azure ACR,
// Google GCR and Artifact Registry) that have a configuration saved in the
// settings store as JSON, under cloud_auth_ecr, cloud_auth_acr and
// cloud_auth_gcr. It returns nil when none is configured.
func buildCloudAuth(db *store.Store, log *logging.Logger) *cloudauth.Registry {
	reg := cloudauth.New()
	add := func(key, name string, build func(raw string) (cloudauth.Provider, error)) {
		raw := loadSettingStr(db, key)
		if raw == "" {
			return
		}
		p, err := build(raw)
		if err != nil {
			log.Warn("cloud registry login not configured: the setting is not valid JSON", "setting", key, "error", err)
			return
		}
		reg.AddProvider(p)
		log.Info("cloud registry login configured", "provider", name)
	}
	add(store.SettingCloudAuthECR, "ecr", func(raw string) (cloudauth.Provider, error) {
		var cfg cloudauth.ECRConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return nil, err
		}
		return cloudauth.NewECR(cfg), nil
	})
	add(store.SettingCloudAuthACR, "acr", func(raw string) (cloudauth.Provider, error) {
		var cfg cloudauth.ACRConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return nil, err
		}
		return cloudauth.NewACR(cfg), nil
	})
	add(store.SettingCloudAuthGCR, "gcr", func(raw string) (cloudauth.Provider, error) {
		var cfg cloudauth.GCRConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return nil, err
		}
		return cloudauth.NewGCR(cfg), nil
	})
	if len(reg.Providers()) == 0 {
		return nil
	}
	return reg
}
