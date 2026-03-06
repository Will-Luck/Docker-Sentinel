package web

import (
	"net/http"
	"path/filepath"
)

// apiBackupTrigger creates an immediate backup.
func (s *Server) apiBackupTrigger(w http.ResponseWriter, r *http.Request) {
	if s.deps.Backup == nil {
		writeError(w, http.StatusNotImplemented, "backup not configured")
		return
	}
	info, err := s.deps.Backup.CreateBackup(r.Context())
	if err != nil {
		s.deps.Log.Error("backup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "backup failed: "+err.Error())
		return
	}
	s.logEvent(r, "backup", "", "Manual backup created: "+info.Filename)
	writeJSON(w, http.StatusOK, info)
}

// apiBackupList returns available backup files.
func (s *Server) apiBackupList(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Backup == nil {
		writeError(w, http.StatusNotImplemented, "backup not configured")
		return
	}
	list, err := s.deps.Backup.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list backups")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// apiBackupDownload serves a backup file for download.
func (s *Server) apiBackupDownload(w http.ResponseWriter, r *http.Request) {
	if s.deps.Backup == nil {
		writeError(w, http.StatusNotImplemented, "backup not configured")
		return
	}
	filename := r.PathValue("filename")
	if filename == "" {
		writeError(w, http.StatusBadRequest, "filename required")
		return
	}
	path, err := s.deps.Backup.FilePath(filename)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(path)+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, path)
}
