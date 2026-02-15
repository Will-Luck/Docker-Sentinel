package web

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
)

// containerName extracts a clean container name from a summary.
func containerName(c ContainerSummary) string {
	if len(c.Names) > 0 {
		name := c.Names[0]
		if len(name) > 0 && name[0] == '/' {
			return name[1:]
		}
		return name
	}
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

// containerPolicy reads the sentinel.policy label, defaulting to "manual".
func containerPolicy(labels map[string]string) string {
	if v, ok := labels["sentinel.policy"]; ok {
		switch v {
		case "auto", "manual", "pinned":
			return v
		}
	}
	return "manual"
}

// isProtectedContainer checks if a container has the sentinel.self=true label.
func (s *Server) isProtectedContainer(ctx context.Context, name string) bool {
	labels := s.getContainerLabels(ctx, name)
	return labels["sentinel.self"] == "true"
}

// resolvedPolicy returns the effective policy: DB override → label fallback.
func (s *Server) resolvedPolicy(labels map[string]string, name string) string {
	if s.deps.Policy != nil {
		if p, ok := s.deps.Policy.GetPolicyOverride(name); ok {
			return p
		}
	}
	return containerPolicy(labels)
}

// getContainerLabels fetches labels for a named container.
func (s *Server) getContainerLabels(ctx context.Context, name string) map[string]string {
	containers, err := s.deps.Docker.ListAllContainers(ctx)
	if err != nil {
		return nil
	}
	for _, c := range containers {
		if containerName(c) == name {
			return c.Labels
		}
	}
	return nil
}

// logEvent appends a log entry if the EventLog dependency is available.
func (s *Server) logEvent(eventType, container, message string) {
	if s.deps.EventLog == nil {
		return
	}
	if err := s.deps.EventLog.AppendLog(LogEntry{
		Type:      eventType,
		Message:   message,
		Container: container,
	}); err != nil {
		s.deps.Log.Warn("failed to persist event log", "type", eventType, "container", container, "error", err)
	}
}

// webReplaceTag replaces the tag portion of an image reference.
// e.g. webReplaceTag("dxflrs/garage:v2.1.0", "v2.2.0") → "dxflrs/garage:v2.2.0"
func webReplaceTag(imageRef, newTag string) string {
	if i := strings.LastIndex(imageRef, ":"); i >= 0 {
		candidate := imageRef[i+1:]
		if !strings.Contains(candidate, "/") {
			return imageRef[:i+1] + newTag
		}
	}
	return imageRef + ":" + newTag
}

// restoreSecrets returns the saved settings if the incoming settings contain any
// masked values (strings ending in "****"). This prevents overwriting real secrets
// with their masked representations when the frontend sends back unchanged channels.
func restoreSecrets(incoming, saved notify.Channel) json.RawMessage {
	var inFields map[string]interface{}
	if err := json.Unmarshal(incoming.Settings, &inFields); err != nil {
		return incoming.Settings
	}

	var savedFields map[string]interface{}
	if err := json.Unmarshal(saved.Settings, &savedFields); err != nil {
		return incoming.Settings
	}

	// Only restore individual masked fields from the saved data,
	// keeping all other fields from the incoming (user-edited) data.
	changed := false
	for k, v := range inFields {
		s, ok := v.(string)
		if ok && strings.HasSuffix(s, "****") {
			if orig, exists := savedFields[k]; exists {
				inFields[k] = orig
				changed = true
			}
		}
	}

	if !changed {
		return incoming.Settings
	}

	merged, err := json.Marshal(inFields)
	if err != nil {
		return incoming.Settings
	}
	return merged
}
