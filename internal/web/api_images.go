package web

import (
	"net/http"
)

// apiListImages returns all Docker images as JSON.
func (s *Server) apiListImages(w http.ResponseWriter, r *http.Request) {
	if s.deps.ImageManager == nil {
		writeError(w, http.StatusServiceUnavailable, "image management not available")
		return
	}
	images, err := s.deps.ImageManager.ListImages(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list images: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": images})
}

// apiPruneImages removes dangling (unused, untagged) images.
func (s *Server) apiPruneImages(w http.ResponseWriter, r *http.Request) {
	if s.deps.ImageManager == nil {
		writeError(w, http.StatusServiceUnavailable, "image management not available")
		return
	}
	result, err := s.deps.ImageManager.PruneImages(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prune images: "+err.Error())
		return
	}
	s.logEvent(r, "image_prune", "", "pruned images")
	writeJSON(w, http.StatusOK, result)
}

// apiRemoveImage removes a single image by ID.
func (s *Server) apiRemoveImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "image ID required")
		return
	}
	if s.deps.ImageManager == nil {
		writeError(w, http.StatusServiceUnavailable, "image management not available")
		return
	}
	if err := s.deps.ImageManager.RemoveImageByID(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove image: "+err.Error())
		return
	}
	shortID := id
	if len(shortID) > 19 {
		shortID = shortID[:19]
	}
	s.logEvent(r, "image_remove", "", "removed image: "+shortID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
