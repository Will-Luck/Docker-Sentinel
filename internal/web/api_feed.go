package web

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"time"
)

// Atom 1.0 XML types.

type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	XMLNS   string      `xml:"xmlns,attr"`
	Title   string      `xml:"title"`
	Links   []atomLink  `xml:"link"`
	Updated string      `xml:"updated"`
	ID      string      `xml:"id"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr,omitempty"`
}

type atomEntry struct {
	Title   string `xml:"title"`
	ID      string `xml:"id"`
	Updated string `xml:"updated"`
	Summary string `xml:"summary"`
}

// apiHistoryFeed serves the last 50 update history records as an Atom 1.0 feed.
// Auth is via a query parameter token, validated against the API token store.
// When auth is disabled (s.deps.Auth == nil), the feed is open.
func (s *Server) apiHistoryFeed(w http.ResponseWriter, r *http.Request) {
	// Validate token when auth is configured.
	if s.deps.Auth != nil && s.deps.Auth.AuthEnabled() {
		token := r.URL.Query().Get("token")
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing token parameter")
			return
		}
		rc := s.deps.Auth.ValidateBearerToken(r.Context(), token)
		if rc == nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
	}

	records, err := s.deps.Store.ListHistory(50, "")
	if err != nil {
		s.deps.Log.Error("failed to list history for feed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load history")
		return
	}

	// Determine the feed-level updated timestamp.
	updated := time.Now().UTC()
	if len(records) > 0 {
		updated = records[0].Timestamp.UTC()
	}

	selfURL := "/api/history/feed"
	if q := r.URL.Query().Get("token"); q != "" {
		selfURL += "?token=" + q
	}

	feed := atomFeed{
		XMLNS:   "http://www.w3.org/2005/Atom",
		Title:   "Docker Sentinel Updates",
		ID:      "urn:sentinel:feed:history",
		Updated: updated.Format(time.RFC3339),
		Links: []atomLink{
			{Href: selfURL, Rel: "self", Type: "application/atom+xml"},
		},
	}

	for _, rec := range records {
		title := fmt.Sprintf("Updated %s: %s \u2192 %s", rec.ContainerName, rec.OldImage, rec.NewImage)

		summary := fmt.Sprintf("Outcome: %s, Duration: %s", rec.Outcome, rec.Duration)
		if rec.Error != "" {
			summary += fmt.Sprintf(", Error: %s", rec.Error)
		}
		if rec.HostName != "" {
			summary += fmt.Sprintf(", Host: %s", rec.HostName)
		}

		id := fmt.Sprintf("urn:sentinel:update:%s:%d", rec.ContainerName, rec.Timestamp.UnixNano())

		feed.Entries = append(feed.Entries, atomEntry{
			Title:   title,
			ID:      id,
			Updated: rec.Timestamp.UTC().Format(time.RFC3339),
			Summary: summary,
		})
	}

	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(feed); err != nil {
		s.deps.Log.Error("failed to encode atom feed", "error", err)
	}
}
