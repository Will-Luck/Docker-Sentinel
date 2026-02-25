package web

import (
	"encoding/json"
	"net/http"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
)

// apiGetNotifyTemplates returns all custom notification templates.
func (s *Server) apiGetNotifyTemplates(w http.ResponseWriter, _ *http.Request) {
	if s.deps.NotifyTemplateStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"templates": map[string]string{}})
		return
	}
	templates, err := s.deps.NotifyTemplateStore.GetAllNotifyTemplates()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load templates")
		return
	}
	if templates == nil {
		templates = make(map[string]string)
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": templates})
}

// apiSaveNotifyTemplate saves a custom notification template for an event type.
func (s *Server) apiSaveNotifyTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventType string `json:"event_type"`
		Template  string `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.EventType == "" {
		writeError(w, http.StatusBadRequest, "event_type is required")
		return
	}

	// Validate the template by doing a test render.
	if req.Template != "" {
		if _, err := notify.RenderPreview(req.Template, req.EventType); err != nil {
			writeError(w, http.StatusBadRequest, "invalid template: "+err.Error())
			return
		}
	}

	if s.deps.NotifyTemplateStore == nil {
		writeError(w, http.StatusServiceUnavailable, "template store not available")
		return
	}

	if err := s.deps.NotifyTemplateStore.SaveNotifyTemplate(req.EventType, req.Template); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save template")
		return
	}
	s.logEvent(r, "settings", "", "notification template updated: "+req.EventType)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiDeleteNotifyTemplate removes a custom template, reverting to default.
func (s *Server) apiDeleteNotifyTemplate(w http.ResponseWriter, r *http.Request) {
	eventType := r.PathValue("type")
	if eventType == "" {
		writeError(w, http.StatusBadRequest, "event type is required")
		return
	}
	if s.deps.NotifyTemplateStore == nil {
		writeError(w, http.StatusServiceUnavailable, "template store not available")
		return
	}
	if err := s.deps.NotifyTemplateStore.DeleteNotifyTemplate(eventType); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete template")
		return
	}
	s.logEvent(r, "settings", "", "notification template reset: "+eventType)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiPreviewNotifyTemplate renders a template with sample data.
func (s *Server) apiPreviewNotifyTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventType string `json:"event_type"`
		Template  string `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Template == "" {
		writeError(w, http.StatusBadRequest, "template is required")
		return
	}
	result, err := notify.RenderPreview(req.Template, req.EventType)
	if err != nil {
		writeError(w, http.StatusBadRequest, "template error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preview": result})
}
