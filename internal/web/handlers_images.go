package web

import "net/http"

// handleImages renders the images management page.
func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	data := pageData{
		Page:       "images",
		QueueCount: len(s.deps.Queue.List()),
	}
	s.withAuth(r, &data)
	s.withCluster(&data)
	s.withPortainer(&data)
	s.renderTemplate(w, "images.html", data)
}
