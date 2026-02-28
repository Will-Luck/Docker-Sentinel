package engine

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// UpdateComposeTag reads a Docker Compose file and updates the image tag for the
// given service. Creates a .bak backup before modifying. Returns nil if the
// service or image line was not found (no-op).
func UpdateComposeTag(composePath, serviceName, newImage string) error {
	data, err := os.ReadFile(composePath)
	if err != nil {
		return fmt.Errorf("read compose file: %w", err)
	}

	content := string(data)

	// Extract just the tag from the new image.
	newTag := ""
	if i := strings.LastIndex(newImage, ":"); i >= 0 {
		newTag = newImage[i+1:]
	}
	if newTag == "" {
		return nil // no tag to update
	}

	// Find the service block and update its image line.
	// Best-effort regex approach for standard compose files.
	updated := updateImageInCompose(content, serviceName, newTag)
	if updated == content {
		return nil // no change
	}

	// Write backup.
	if err := os.WriteFile(composePath+".bak", data, 0600); err != nil {
		return fmt.Errorf("write compose backup: %w", err)
	}

	// Write updated file.
	if err := os.WriteFile(composePath, []byte(updated), 0600); err != nil {
		return fmt.Errorf("write compose file: %w", err)
	}

	return nil
}

// updateImageInCompose finds the image line for a service and updates its tag.
func updateImageInCompose(content, serviceName, newTag string) string {
	lines := strings.Split(content, "\n")
	inService := false
	serviceIndent := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect service name (e.g. "  servicename:" at indent under services).
		if trimmed == serviceName+":" {
			inService = true
			serviceIndent = len(line) - len(strings.TrimLeft(line, " "))
			continue
		}

		if inService {
			currentIndent := len(line) - len(strings.TrimLeft(line, " "))

			// Same or lesser indent on non-empty line means we left the block.
			if trimmed != "" && currentIndent <= serviceIndent {
				inService = false
				continue
			}

			// Match "image: repo:tag" or "image: repo"
			re := regexp.MustCompile(`^(\s+image:\s*\S+?):(\S+)\s*$`)
			if m := re.FindStringSubmatch(line); m != nil {
				lines[i] = m[1] + ":" + newTag
				return strings.Join(lines, "\n")
			}
		}
	}

	return content
}
