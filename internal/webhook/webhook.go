// Package webhook parses inbound webhook payloads from Docker Hub, GHCR,
// and generic CI/CD pipelines into a normalised Payload struct.
package webhook

import (
	"encoding/json"
	"errors"
	"strings"
)

// Payload represents a parsed webhook payload.
type Payload struct {
	Image    string // e.g. "nginx", "ghcr.io/user/repo"
	Tag      string // e.g. "latest", "v1.2.3"
	Source   string // "dockerhub", "ghcr", "generic", "unknown"
	RawEvent string // original event type if available
}

// ErrEmptyBody is returned when the request body is empty.
var ErrEmptyBody = errors.New("empty request body")

// Parse attempts to detect and parse a webhook payload from various sources.
// It tries Docker Hub format, GHCR format, and generic format in order.
// If the body is valid JSON but doesn't match any known format, a Payload
// with Source "unknown" is returned (no error).
func Parse(body []byte) (*Payload, error) {
	if len(body) == 0 {
		return nil, ErrEmptyBody
	}

	// Quick check: body must be valid JSON.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("invalid JSON: " + err.Error())
	}

	// Try Docker Hub format first — presence of "push_data" is the discriminator.
	if _, ok := raw["push_data"]; ok {
		if p, err := parseDockerHub(body); err == nil {
			return p, nil
		}
	}

	// Try GHCR (GitHub package event) — presence of "package" key.
	if _, ok := raw["package"]; ok {
		if p, err := parseGHCR(body); err == nil {
			return p, nil
		}
	}

	// Try generic format — presence of "image" key.
	if _, ok := raw["image"]; ok {
		if p, err := parseGeneric(body); err == nil {
			return p, nil
		}
	}

	// Valid JSON but unrecognised format.
	return &Payload{Source: "unknown"}, nil
}

// parseDockerHub handles Docker Hub webhook payloads.
//
//	{
//	    "push_data": {"tag": "latest"},
//	    "repository": {"repo_name": "library/nginx", "name": "nginx"}
//	}
func parseDockerHub(body []byte) (*Payload, error) {
	var hub struct {
		PushData struct {
			Tag string `json:"tag"`
		} `json:"push_data"`
		Repository struct {
			RepoName string `json:"repo_name"`
			Name     string `json:"name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &hub); err != nil {
		return nil, err
	}

	image := hub.Repository.RepoName
	if image == "" {
		image = hub.Repository.Name
	}
	if image == "" {
		return nil, errors.New("dockerhub: missing repository name")
	}

	return &Payload{
		Image:    image,
		Tag:      hub.PushData.Tag,
		Source:   "dockerhub",
		RawEvent: "push",
	}, nil
}

// parseGHCR handles GitHub Container Registry webhook payloads (package event).
//
//	{
//	    "action": "published",
//	    "package": {
//	        "name": "my-app",
//	        "package_version": {"container_metadata": {"tag": {"name": "v1.0.0"}}},
//	        "namespace": "username",
//	        "package_type": "container"
//	    }
//	}
func parseGHCR(body []byte) (*Payload, error) {
	var gh struct {
		Action  string `json:"action"`
		Package struct {
			Name           string `json:"name"`
			Namespace      string `json:"namespace"`
			PackageType    string `json:"package_type"`
			PackageVersion struct {
				ContainerMetadata struct {
					Tag struct {
						Name string `json:"name"`
					} `json:"tag"`
				} `json:"container_metadata"`
			} `json:"package_version"`
		} `json:"package"`
	}
	if err := json.Unmarshal(body, &gh); err != nil {
		return nil, err
	}

	if gh.Package.Name == "" {
		return nil, errors.New("ghcr: missing package name")
	}

	// Build the full GHCR image reference.
	image := "ghcr.io/" + gh.Package.Namespace + "/" + gh.Package.Name
	if gh.Package.Namespace == "" {
		image = "ghcr.io/" + gh.Package.Name
	}

	return &Payload{
		Image:    image,
		Tag:      gh.Package.PackageVersion.ContainerMetadata.Tag.Name,
		Source:   "ghcr",
		RawEvent: gh.Action,
	}, nil
}

// parseGeneric handles simple CI/CD webhook payloads.
//
//	{"image": "nginx:latest"}
//
// or
//
//	{"image": "nginx", "tag": "v1.2.3"}
func parseGeneric(body []byte) (*Payload, error) {
	var gen struct {
		Image string `json:"image"`
		Tag   string `json:"tag"`
	}
	if err := json.Unmarshal(body, &gen); err != nil {
		return nil, err
	}

	if gen.Image == "" {
		return nil, errors.New("generic: missing image field")
	}

	image := gen.Image
	tag := gen.Tag

	// If no separate tag field, try splitting "image:tag".
	if tag == "" {
		if idx := strings.LastIndex(image, ":"); idx >= 0 {
			candidate := image[idx+1:]
			// Make sure we're splitting on tag, not on a port in the registry host.
			if !strings.Contains(candidate, "/") {
				tag = candidate
				image = image[:idx]
			}
		}
	}

	return &Payload{
		Image:  image,
		Tag:    tag,
		Source: "generic",
	}, nil
}
