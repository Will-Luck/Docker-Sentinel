package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// rejectedCredentials lists registries whose stored credential was rejected.
// It is a variable so tests can supply a list.
var rejectedCredentials = registry.RejectedCredentials

// cloudProvider describes one cloud registry login: the setting that holds
// its JSON, its plain fields and its secret fields.
type cloudProvider struct {
	name    string
	setting string
	plain   []string
	secret  []string
}

var cloudProviders = []cloudProvider{
	{"ecr", store.SettingCloudAuthECR, []string{"region", "access_key", "account_id"}, []string{"secret_key"}},
	{"acr", store.SettingCloudAuthACR, []string{"tenant_id", "client_id", "login_server"}, []string{"client_secret"}},
	{"gcr", store.SettingCloudAuthGCR, nil, []string{"service_account_json"}},
}

// required are the fields a provider needs before it can log in.
var cloudRequired = map[string][]string{
	"ecr": {"region", "access_key", "secret_key"},
	"acr": {"tenant_id", "client_id", "client_secret", "login_server"},
	"gcr": {"service_account_json"},
}

// apiGetCloudAuth returns each cloud registry login with its plain fields.
// Secrets are never returned: a <field>_set flag says whether one is stored.
func (s *Server) apiGetCloudAuth(w http.ResponseWriter, _ *http.Request) {
	out := map[string]map[string]any{}
	for _, p := range cloudProviders {
		entry := map[string]any{"configured": false}
		var raw string
		if s.deps.SettingsStore != nil {
			raw, _ = s.deps.SettingsStore.LoadSetting(p.setting)
		}
		if raw != "" {
			var stored map[string]string
			if json.Unmarshal([]byte(raw), &stored) == nil {
				entry["configured"] = true
				for _, f := range p.plain {
					entry[f] = stored[f]
				}
				for _, f := range p.secret {
					entry[f+"_set"] = stored[f] != ""
				}
			}
		}
		out[p.name] = entry
	}
	writeJSON(w, http.StatusOK, out)
}

// apiSaveCloudAuth saves the cloud registry logins sent. A provider that is
// null is cleared. A secret left blank keeps the stored one. The providers
// are read at startup, so a restart applies the change.
func (s *Server) apiSaveCloudAuth(w http.ResponseWriter, r *http.Request) {
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	var body map[string]*map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	type change struct {
		setting, value string
	}
	var changes []change
	for _, p := range cloudProviders {
		sent, present := body[p.name]
		if !present {
			continue
		}
		if sent == nil {
			changes = append(changes, change{p.setting, ""})
			continue
		}
		stored := map[string]string{}
		if raw, _ := s.deps.SettingsStore.LoadSetting(p.setting); raw != "" {
			_ = json.Unmarshal([]byte(raw), &stored)
		}
		merged := map[string]string{}
		for _, f := range p.plain {
			merged[f] = strings.TrimSpace((*sent)[f])
		}
		for _, f := range p.secret {
			v := (*sent)[f]
			if v == "" {
				v = stored[f]
			}
			merged[f] = v
		}
		for _, f := range cloudRequired[p.name] {
			if merged[f] == "" {
				writeError(w, http.StatusBadRequest, p.name+": "+f+" is required")
				return
			}
		}
		if p.name == "gcr" && !json.Valid([]byte(merged["service_account_json"])) {
			writeError(w, http.StatusBadRequest, "gcr: service_account_json must be the JSON key file")
			return
		}
		data, _ := json.Marshal(merged)
		changes = append(changes, change{p.setting, string(data)})
	}
	for _, c := range changes {
		if err := s.deps.SettingsStore.SaveSetting(c.setting, c.value); err != nil {
			s.deps.Log.Error("failed to save cloud registry login", "setting", c.setting, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	s.logEvent(r, "settings", "", "Cloud registry logins updated")
	writeJSON(w, http.StatusOK, map[string]string{"message": "Saved. Restart Sentinel to apply the change.", "restart_required": "true"})
}
