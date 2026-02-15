package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
)

// apiGetRegistryCredentials returns stored credentials (masked) merged with rate limit status.
func (s *Server) apiGetRegistryCredentials(w http.ResponseWriter, r *http.Request) {
	type registryInfo struct {
		Credential *RegistryCredential `json:"credential,omitempty"`
		RateLimit  *RateLimitStatus    `json:"rate_limit,omitempty"`
	}

	result := make(map[string]*registryInfo)

	// Load credentials.
	if s.deps.RegistryCredentials != nil {
		creds, err := s.deps.RegistryCredentials.GetRegistryCredentials()
		if err != nil {
			s.deps.Log.Error("failed to load registry credentials", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load registry credentials")
			return
		}
		for _, c := range creds {
			masked := c
			if len(c.Secret) > 4 {
				masked.Secret = c.Secret[:4] + "****"
			} else if c.Secret != "" {
				masked.Secret = "****"
			}
			info := &registryInfo{Credential: &masked}
			result[c.Registry] = info
		}
	}

	// Merge rate limit status.
	if s.deps.RateTracker != nil {
		for _, st := range s.deps.RateTracker.Status() {
			info, ok := result[st.Registry]
			if !ok {
				info = &registryInfo{}
				result[st.Registry] = info
			}
			stCopy := RateLimitStatus(st)
			info.RateLimit = &stCopy
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// apiSaveRegistryCredentials saves registry credentials.
func (s *Server) apiSaveRegistryCredentials(w http.ResponseWriter, r *http.Request) {
	if s.deps.RegistryCredentials == nil {
		writeError(w, http.StatusNotImplemented, "registry credentials not available")
		return
	}

	var creds []RegistryCredential
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Restore masked secrets from saved credentials.
	existing, _ := s.deps.RegistryCredentials.GetRegistryCredentials()
	savedMap := make(map[string]RegistryCredential, len(existing))
	for _, c := range existing {
		savedMap[c.ID] = c
	}
	for i, c := range creds {
		if strings.HasSuffix(c.Secret, "****") {
			if old, ok := savedMap[c.ID]; ok {
				creds[i].Secret = old.Secret
			}
		}
	}

	// Validate credentials.
	seen := make(map[string]bool, len(creds))
	for _, c := range creds {
		if strings.TrimSpace(c.Registry) == "" {
			writeError(w, http.StatusBadRequest, "registry cannot be empty")
			return
		}
		if strings.TrimSpace(c.Username) == "" {
			writeError(w, http.StatusBadRequest, "username cannot be empty for "+c.Registry)
			return
		}
		if strings.TrimSpace(c.Secret) == "" {
			writeError(w, http.StatusBadRequest, "secret cannot be empty for "+c.Registry)
			return
		}
		norm := registry.NormaliseRegistryHost(c.Registry)
		if seen[norm] {
			writeError(w, http.StatusBadRequest, "duplicate registry: "+c.Registry)
			return
		}
		seen[norm] = true
	}

	if err := s.deps.RegistryCredentials.SetRegistryCredentials(creds); err != nil {
		s.deps.Log.Error("failed to save registry credentials", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save registry credentials")
		return
	}

	s.logEvent(r, "settings", "", "Registry credentials updated")

	// Probe each registry in the background to discover rate limits immediately.
	if s.deps.RateTracker != nil {
		for _, c := range creds {
			go func(cred RegistryCredential) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if err := s.deps.RateTracker.ProbeAndRecord(ctx, cred.Registry, cred); err != nil {
					s.deps.Log.Warn("rate limit probe failed", "registry", cred.Registry, "error", err)
				} else {
					s.deps.Log.Info("rate limit probe complete", "registry", cred.Registry)
				}
			}(c)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "registry credentials saved",
	})
}

// apiDeleteRegistryCredential removes a single credential by ID.
func (s *Server) apiDeleteRegistryCredential(w http.ResponseWriter, r *http.Request) {
	if s.deps.RegistryCredentials == nil {
		writeError(w, http.StatusNotImplemented, "registry credentials not available")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "credential id required")
		return
	}

	creds, err := s.deps.RegistryCredentials.GetRegistryCredentials()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load credentials")
		return
	}

	found := false
	filtered := make([]RegistryCredential, 0, len(creds))
	for _, c := range creds {
		if c.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, c)
	}

	if !found {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	if err := s.deps.RegistryCredentials.SetRegistryCredentials(filtered); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save credentials")
		return
	}

	s.logEvent(r, "settings", "", "Registry credential removed")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiTestRegistryCredential validates a credential by making a lightweight v2 API call.
func (s *Server) apiTestRegistryCredential(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string `json:"id"`
		Registry string `json:"registry"`
		Username string `json:"username"`
		Secret   string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Registry == "" || body.Username == "" || body.Secret == "" {
		writeError(w, http.StatusBadRequest, "registry, username, and secret are required")
		return
	}

	// If secret is masked, try to restore from saved credentials.
	if strings.HasSuffix(body.Secret, "****") && s.deps.RegistryCredentials != nil {
		existing, _ := s.deps.RegistryCredentials.GetRegistryCredentials()
		restored := false
		// Prefer lookup by ID (stable even if registry field was edited).
		if body.ID != "" {
			for _, c := range existing {
				if c.ID == body.ID {
					body.Secret = c.Secret
					restored = true
					break
				}
			}
		}
		// Fall back to registry name lookup.
		if !restored {
			for _, c := range existing {
				if c.Registry == body.Registry {
					body.Secret = c.Secret
					break
				}
			}
		}
	}

	// Test Docker Hub auth endpoint.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	authURL := "https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/alpine:pull"
	if body.Registry != "docker.io" {
		// For non-Docker Hub, try GET /v2/ with basic auth.
		authURL = "https://" + body.Registry + "/v2/"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "invalid registry URL"})
		return
	}
	req.SetBasicAuth(body.Username, body.Secret)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		// Probe rate limits in the background now that we know the creds are valid.
		if s.deps.RateTracker != nil {
			go func() {
				probeCtx, probeCancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer probeCancel()
				cred := RegistryCredential{Registry: body.Registry, Username: body.Username, Secret: body.Secret}
				if err := s.deps.RateTracker.ProbeAndRecord(probeCtx, body.Registry, cred); err != nil {
					s.deps.Log.Warn("rate limit probe after test failed", "registry", body.Registry, "error", err)
				} else {
					s.deps.Log.Info("rate limit probe after test complete", "registry", body.Registry)
				}
			}()
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Credentials valid"})
		return
	}
	if resp.StatusCode == http.StatusUnauthorized {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "Invalid credentials (401 Unauthorized)"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": fmt.Sprintf("Unexpected status: %d", resp.StatusCode)})
}

// apiGetRateLimits returns rate limit status for all registries (lower permission, for dashboard polling).
func (s *Server) apiGetRateLimits(w http.ResponseWriter, r *http.Request) {
	if s.deps.RateTracker == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"health":     "ok",
			"registries": []RateLimitStatus{},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"health":     s.deps.RateTracker.OverallHealth(),
		"registries": s.deps.RateTracker.Status(),
	})
}

// apiGetGHCRAlternatives returns all known GHCR alternatives for dashboard badges.
func (s *Server) apiGetGHCRAlternatives(w http.ResponseWriter, r *http.Request) {
	if s.deps.GHCRCache == nil {
		writeJSON(w, http.StatusOK, []GHCRAlternative{})
		return
	}

	alts := s.deps.GHCRCache.All()
	if alts == nil {
		alts = []GHCRAlternative{}
	}
	writeJSON(w, http.StatusOK, alts)
}

// apiGetContainerGHCR returns GHCR alternative info for a single container.
func (s *Server) apiGetContainerGHCR(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	if s.deps.GHCRCache == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}

	// Find the container to get its image reference.
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}

	var imageRef string
	for _, c := range containers {
		if containerName(c) == name {
			imageRef = c.Image
			break
		}
	}
	if imageRef == "" {
		writeError(w, http.StatusNotFound, "container not found: "+name)
		return
	}

	repo := registry.RepoPath(imageRef)
	tag := registry.ExtractTag(imageRef)
	if tag == "" {
		tag = "latest"
	}

	alt, ok := s.deps.GHCRCache.Get(repo, tag)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "checked": false})
		return
	}

	writeJSON(w, http.StatusOK, alt)
}

// apiSwitchToGHCR triggers a container migration from Docker Hub to GHCR.
func (s *Server) apiSwitchToGHCR(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	if s.isProtectedContainer(r.Context(), name) {
		writeError(w, http.StatusForbidden, "cannot switch sentinel itself")
		return
	}

	if s.deps.GHCRCache == nil {
		writeError(w, http.StatusNotImplemented, "GHCR detection not available")
		return
	}

	// Find container.
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}

	var containerID, imageRef string
	for _, c := range containers {
		if containerName(c) == name {
			containerID = c.ID
			imageRef = c.Image
			break
		}
	}
	if containerID == "" {
		writeError(w, http.StatusNotFound, "container not found: "+name)
		return
	}

	repo := registry.RepoPath(imageRef)
	tag := registry.ExtractTag(imageRef)
	if tag == "" {
		tag = "latest"
	}

	alt, ok := s.deps.GHCRCache.Get(repo, tag)
	if !ok || !alt.Available {
		writeError(w, http.StatusBadRequest, "no GHCR alternative available for "+name)
		return
	}

	// Build the target GHCR image reference.
	ghcrImage := alt.GHCRImage + ":" + alt.Tag

	// Trigger migration in background using the existing UpdateContainer lifecycle.
	go func() {
		err := s.deps.Updater.UpdateContainer(context.Background(), containerID, name, ghcrImage)
		if err != nil {
			s.deps.Log.Error("GHCR switch failed", "name", name, "ghcr_image", ghcrImage, "error", err)
			return
		}
		s.deps.Log.Info("GHCR switch complete", "name", name, "ghcr_image", ghcrImage)
	}()

	s.logEvent(r, "ghcr_switch", name, "Switching to GHCR: "+ghcrImage)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "started",
		"name":       name,
		"ghcr_image": ghcrImage,
		"message":    "migration to GHCR started for " + name,
	})
}
