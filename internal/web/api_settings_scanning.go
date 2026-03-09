package web

import (
	"encoding/json"
	"net/http"

	"github.com/Will-Luck/Docker-Sentinel/internal/scanner"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
)

// apiScannerSettings returns the current scanner (Trivy) configuration.
func (s *Server) apiScannerSettings(w http.ResponseWriter, _ *http.Request) {
	result := map[string]string{
		"mode":       string(scanner.ScanDisabled),
		"threshold":  string(scanner.SeverityHigh),
		"trivy_path": "trivy",
	}

	if s.deps.SettingsStore != nil {
		keys := map[string]string{
			"mode":       store.SettingScannerMode,
			"threshold":  store.SettingScannerThreshold,
			"trivy_path": store.SettingTrivyPath,
		}
		for field, dbKey := range keys {
			if v, err := s.deps.SettingsStore.LoadSetting(dbKey); err == nil && v != "" {
				result[field] = v
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// apiScannerSettingsSave persists scanner configuration changes.
func (s *Server) apiScannerSettingsSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode      string `json:"mode"`
		Threshold string `json:"threshold"`
		TrivyPath string `json:"trivy_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusInternalServerError, "settings store not available")
		return
	}

	// Validate mode.
	if req.Mode != "" {
		switch req.Mode {
		case string(scanner.ScanDisabled), string(scanner.ScanPreUpdate), string(scanner.ScanPostUpdate):
			// valid
		default:
			writeError(w, http.StatusBadRequest, "invalid scan mode")
			return
		}
		if err := s.deps.SettingsStore.SaveSetting(store.SettingScannerMode, req.Mode); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}

	// Validate severity threshold.
	if req.Threshold != "" {
		switch scanner.Severity(req.Threshold) {
		case scanner.SeverityCritical, scanner.SeverityHigh, scanner.SeverityMedium, scanner.SeverityLow:
			// valid
		default:
			writeError(w, http.StatusBadRequest, "invalid severity threshold")
			return
		}
		if err := s.deps.SettingsStore.SaveSetting(store.SettingScannerThreshold, req.Threshold); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}

	// Trivy path (no validation beyond non-empty).
	if req.TrivyPath != "" {
		if err := s.deps.SettingsStore.SaveSetting(store.SettingTrivyPath, req.TrivyPath); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}

	s.logEvent(r, "settings", "", "Scanner settings updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// apiVerifierSettings returns the current verifier (cosign) configuration.
func (s *Server) apiVerifierSettings(w http.ResponseWriter, _ *http.Request) {
	result := map[string]string{
		"mode":        string(verify.ModeDisabled),
		"cosign_path": "cosign",
		"keyless":     "false",
		"key_path":    "",
	}

	if s.deps.SettingsStore != nil {
		keys := map[string]string{
			"mode":        store.SettingVerifyMode,
			"cosign_path": store.SettingCosignPath,
			"keyless":     store.SettingCosignKeyless,
			"key_path":    store.SettingCosignKeyPath,
		}
		for field, dbKey := range keys {
			if v, err := s.deps.SettingsStore.LoadSetting(dbKey); err == nil && v != "" {
				result[field] = v
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// apiVerifierSettingsSave persists verifier configuration changes.
func (s *Server) apiVerifierSettingsSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode       string `json:"mode"`
		CosignPath string `json:"cosign_path"`
		Keyless    *bool  `json:"keyless"`
		KeyPath    string `json:"key_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusInternalServerError, "settings store not available")
		return
	}

	// Validate mode.
	if req.Mode != "" {
		switch req.Mode {
		case string(verify.ModeDisabled), string(verify.ModeWarn), string(verify.ModeEnforce):
			// valid
		default:
			writeError(w, http.StatusBadRequest, "invalid verify mode")
			return
		}
		if err := s.deps.SettingsStore.SaveSetting(store.SettingVerifyMode, req.Mode); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}

	if req.CosignPath != "" {
		if err := s.deps.SettingsStore.SaveSetting(store.SettingCosignPath, req.CosignPath); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}

	if req.Keyless != nil {
		val := "false"
		if *req.Keyless {
			val = "true"
		}
		if err := s.deps.SettingsStore.SaveSetting(store.SettingCosignKeyless, val); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}

	// key_path can be empty (clearing the key path), so save unconditionally
	// when the field is present in the request. Use a sentinel check: the
	// JSON decoder will set it to "" if omitted, but that is also a valid
	// "clear" value, so always persist.
	if req.KeyPath != "" || req.Mode != "" {
		// Only persist key_path when explicitly provided (non-empty) or when
		// mode is being changed (which implies a full config save).
		if req.KeyPath != "" {
			if err := s.deps.SettingsStore.SaveSetting(store.SettingCosignKeyPath, req.KeyPath); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to save setting")
				return
			}
		}
	}

	s.logEvent(r, "settings", "", "Verifier settings updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}
