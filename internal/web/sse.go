package web

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// apiSSE streams server-sent events to the client. The connection stays open
// until the client disconnects or the server shuts down.
func (s *Server) apiSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, cancel := s.deps.EventBus.Subscribe()
	defer cancel()

	// Send an initial connected event so the client knows the stream is live.
	fmt.Fprint(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(evt)
			if err != nil {
				s.deps.Log.Warn("failed to marshal SSE event", "error", err)
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, data)
			flusher.Flush()

		case <-r.Context().Done():
			return
		}
	}
}
