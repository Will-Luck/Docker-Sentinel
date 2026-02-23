package web

import (
	"net/http"
	"strconv"
)

func (s *Server) handlePortainer(w http.ResponseWriter, r *http.Request) {
	data := pageData{Page: "portainer"}
	s.withAuth(r, &data)
	s.withCluster(&data)
	s.withPortainer(&data)
	s.renderTemplate(w, "portainer.html", data)
}

func (s *Server) apiPortainerEndpoints(w http.ResponseWriter, r *http.Request) {
	if s.deps.Portainer == nil {
		writeJSON(w, http.StatusOK, []PortainerEndpoint{})
		return
	}
	endpoints, err := s.deps.Portainer.AllEndpoints(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if endpoints == nil {
		endpoints = []PortainerEndpoint{}
	}
	writeJSON(w, http.StatusOK, endpoints)
}

func (s *Server) apiPortainerContainers(w http.ResponseWriter, r *http.Request) {
	if s.deps.Portainer == nil {
		writeJSON(w, http.StatusOK, []PortainerContainerInfo{})
		return
	}
	idStr := r.PathValue("id")
	endpointID, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}
	containers, err := s.deps.Portainer.EndpointContainers(r.Context(), endpointID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if containers == nil {
		containers = []PortainerContainerInfo{}
	}
	writeJSON(w, http.StatusOK, containers)
}
