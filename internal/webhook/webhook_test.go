package webhook

import (
	"testing"
)

func TestParse_DockerHub(t *testing.T) {
	body := []byte(`{
		"push_data": {"tag": "latest"},
		"repository": {"repo_name": "library/nginx", "name": "nginx"}
	}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Source != "dockerhub" {
		t.Errorf("source = %q, want %q", p.Source, "dockerhub")
	}
	if p.Image != "library/nginx" {
		t.Errorf("image = %q, want %q", p.Image, "library/nginx")
	}
	if p.Tag != "latest" {
		t.Errorf("tag = %q, want %q", p.Tag, "latest")
	}
	if p.RawEvent != "push" {
		t.Errorf("rawEvent = %q, want %q", p.RawEvent, "push")
	}
}

func TestParse_DockerHub_NameFallback(t *testing.T) {
	// When repo_name is empty, fall back to name field.
	body := []byte(`{
		"push_data": {"tag": "v2.0"},
		"repository": {"name": "myapp"}
	}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Source != "dockerhub" {
		t.Errorf("source = %q, want %q", p.Source, "dockerhub")
	}
	if p.Image != "myapp" {
		t.Errorf("image = %q, want %q", p.Image, "myapp")
	}
	if p.Tag != "v2.0" {
		t.Errorf("tag = %q, want %q", p.Tag, "v2.0")
	}
}

func TestParse_GHCR(t *testing.T) {
	body := []byte(`{
		"action": "published",
		"package": {
			"name": "my-app",
			"package_version": {
				"container_metadata": {
					"tag": {"name": "v1.0.0"}
				}
			},
			"namespace": "username",
			"package_type": "container"
		}
	}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Source != "ghcr" {
		t.Errorf("source = %q, want %q", p.Source, "ghcr")
	}
	if p.Image != "ghcr.io/username/my-app" {
		t.Errorf("image = %q, want %q", p.Image, "ghcr.io/username/my-app")
	}
	if p.Tag != "v1.0.0" {
		t.Errorf("tag = %q, want %q", p.Tag, "v1.0.0")
	}
	if p.RawEvent != "published" {
		t.Errorf("rawEvent = %q, want %q", p.RawEvent, "published")
	}
}

func TestParse_GHCR_NoNamespace(t *testing.T) {
	body := []byte(`{
		"action": "published",
		"package": {
			"name": "my-app",
			"package_version": {
				"container_metadata": {
					"tag": {"name": "latest"}
				}
			},
			"package_type": "container"
		}
	}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Image != "ghcr.io/my-app" {
		t.Errorf("image = %q, want %q", p.Image, "ghcr.io/my-app")
	}
}

func TestParse_GenericImageTag(t *testing.T) {
	body := []byte(`{"image": "nginx", "tag": "v1.2.3"}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Source != "generic" {
		t.Errorf("source = %q, want %q", p.Source, "generic")
	}
	if p.Image != "nginx" {
		t.Errorf("image = %q, want %q", p.Image, "nginx")
	}
	if p.Tag != "v1.2.3" {
		t.Errorf("tag = %q, want %q", p.Tag, "v1.2.3")
	}
}

func TestParse_GenericImageColon(t *testing.T) {
	body := []byte(`{"image": "nginx:latest"}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Source != "generic" {
		t.Errorf("source = %q, want %q", p.Source, "generic")
	}
	if p.Image != "nginx" {
		t.Errorf("image = %q, want %q", p.Image, "nginx")
	}
	if p.Tag != "latest" {
		t.Errorf("tag = %q, want %q", p.Tag, "latest")
	}
}

func TestParse_GenericRegistryWithPort(t *testing.T) {
	// "registry.example.com:5000/myapp" — the colon is part of the host, not a tag.
	body := []byte(`{"image": "registry.example.com:5000/myapp"}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Image != "registry.example.com:5000/myapp" {
		t.Errorf("image = %q, want %q", p.Image, "registry.example.com:5000/myapp")
	}
	if p.Tag != "" {
		t.Errorf("tag = %q, want empty", p.Tag)
	}
}

func TestParse_GenericRegistryWithPortAndTag(t *testing.T) {
	body := []byte(`{"image": "registry.example.com:5000/myapp:v2"}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Image != "registry.example.com:5000/myapp" {
		t.Errorf("image = %q, want %q", p.Image, "registry.example.com:5000/myapp")
	}
	if p.Tag != "v2" {
		t.Errorf("tag = %q, want %q", p.Tag, "v2")
	}
}

func TestParse_EmptyBody(t *testing.T) {
	_, err := Parse(nil)
	if err != ErrEmptyBody {
		t.Errorf("error = %v, want ErrEmptyBody", err)
	}

	_, err = Parse([]byte{})
	if err != ErrEmptyBody {
		t.Errorf("error = %v, want ErrEmptyBody", err)
	}
}

func TestParse_MalformedJSON(t *testing.T) {
	_, err := Parse([]byte(`{not json`))
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestParse_UnrecognisedFormat(t *testing.T) {
	body := []byte(`{"foo": "bar", "baz": 42}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Source != "unknown" {
		t.Errorf("source = %q, want %q", p.Source, "unknown")
	}
	if p.Image != "" {
		t.Errorf("image = %q, want empty", p.Image)
	}
}

func TestParse_DockerHub_MissingRepo(t *testing.T) {
	// push_data present but repository is empty — should fall through to unknown.
	body := []byte(`{"push_data": {"tag": "latest"}, "repository": {}}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Falls through dockerhub (fails) → no "package" → no "image" → unknown.
	if p.Source != "unknown" {
		t.Errorf("source = %q, want %q", p.Source, "unknown")
	}
}

func TestParse_GHCR_MissingPackageName(t *testing.T) {
	body := []byte(`{
		"action": "published",
		"package": {
			"package_type": "container"
		}
	}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// GHCR parse fails (no name) → no "image" → unknown.
	if p.Source != "unknown" {
		t.Errorf("source = %q, want %q", p.Source, "unknown")
	}
}

func TestParse_GenericEmptyImage(t *testing.T) {
	body := []byte(`{"image": ""}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Generic parse fails (empty image) → unknown.
	if p.Source != "unknown" {
		t.Errorf("source = %q, want %q", p.Source, "unknown")
	}
}

func TestParse_GenericOnlyTag(t *testing.T) {
	// Image without tag, tag provided separately.
	body := []byte(`{"image": "ghcr.io/user/app", "tag": "sha-abc123"}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Image != "ghcr.io/user/app" {
		t.Errorf("image = %q, want %q", p.Image, "ghcr.io/user/app")
	}
	if p.Tag != "sha-abc123" {
		t.Errorf("tag = %q, want %q", p.Tag, "sha-abc123")
	}
}

func TestParse_GHCR_NoTag(t *testing.T) {
	// GHCR event without tag metadata (e.g. digest-only push).
	body := []byte(`{
		"action": "published",
		"package": {
			"name": "my-app",
			"namespace": "org",
			"package_type": "container",
			"package_version": {}
		}
	}`)

	p, err := Parse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Source != "ghcr" {
		t.Errorf("source = %q, want %q", p.Source, "ghcr")
	}
	if p.Image != "ghcr.io/org/my-app" {
		t.Errorf("image = %q, want %q", p.Image, "ghcr.io/org/my-app")
	}
	if p.Tag != "" {
		t.Errorf("tag = %q, want empty", p.Tag)
	}
}
