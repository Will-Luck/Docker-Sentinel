package web

import (
	"errors"
	"net/http"

	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
)

// swapDiscarder discards the journal record of a container swap that
// Sentinel cannot finish on its own. The engine's Updater implements it.
type swapDiscarder interface {
	DiscardSwap(name string) error
}

var _ swapDiscarder = (*engine.Updater)(nil)

// apiDiscardSwap handles DELETE /api/swaps/{name}: after the user has fixed
// the containers of an unfinished update by hand (one working copy named
// {name}), it discards the swap record so Sentinel stops trying to finish
// it. The engine logs the record before it goes. Admin only.
func (s *Server) apiDiscardSwap(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !isValidContainerName(name) {
		writeError(w, http.StatusBadRequest, "invalid container name")
		return
	}
	d, ok := s.deps.Updater.(swapDiscarder)
	if !ok {
		writeError(w, http.StatusNotImplemented, "swap records are not available")
		return
	}
	switch err := d.DiscardSwap(name); {
	case errors.Is(err, engine.ErrNoSwap):
		writeError(w, http.StatusNotFound, "no unfinished swap is recorded for "+name)
		return
	case errors.Is(err, engine.ErrUpdateInProgress):
		writeError(w, http.StatusConflict, "an update of "+name+" is running")
		return
	case err != nil:
		s.deps.Log.Error("failed to discard swap record", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to discard the swap record")
		return
	}
	s.logEvent(r, "swap-discard", name, "Discarded the unfinished update record of "+name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "discarded", "name": name})
}
