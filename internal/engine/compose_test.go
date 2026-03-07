package engine

import (
	"os"
	"path/filepath"
	"testing"
)

const testComposeContent = `services:
  nginx:
    image: nginx:1.24
    ports:
      - "80:80"
  redis:
    image: redis:7
`

func writeComposeFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatalf("write compose file: %v", err)
	}
	return p
}

func TestUpdateComposeTag_HappyPath(t *testing.T) {
	p := writeComposeFile(t, testComposeContent)

	err := UpdateComposeTag(p, "nginx", "nginx:1.25")
	if err != nil {
		t.Fatalf("UpdateComposeTag() error: %v", err)
	}

	// Verify the file was updated.
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	content := string(data)
	if got := "image: nginx:1.25"; !containsString(content, got) {
		t.Errorf("updated file should contain %q, got:\n%s", got, content)
	}
	// Original tag should be gone.
	if containsString(content, "nginx:1.24") {
		t.Errorf("updated file still contains old tag nginx:1.24")
	}

	// Verify .bak was created with original content.
	bakData, err := os.ReadFile(p + ".bak")
	if err != nil {
		t.Fatalf("read .bak file: %v", err)
	}
	if string(bakData) != testComposeContent {
		t.Errorf(".bak content differs from original")
	}
}

func TestUpdateComposeTag_ServiceNotFound(t *testing.T) {
	p := writeComposeFile(t, testComposeContent)

	err := UpdateComposeTag(p, "postgres", "postgres:16")
	if err != nil {
		t.Fatalf("UpdateComposeTag() error: %v", err)
	}

	// File should be unchanged (no .bak created).
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != testComposeContent {
		t.Error("file should be unchanged when service not found")
	}
	if _, err := os.Stat(p + ".bak"); err == nil {
		t.Error(".bak should not exist when no changes were made")
	}
}

func TestUpdateComposeTag_NoTagInNewImage(t *testing.T) {
	p := writeComposeFile(t, testComposeContent)

	// newImage without a tag should be a no-op.
	err := UpdateComposeTag(p, "nginx", "nginx")
	if err != nil {
		t.Fatalf("UpdateComposeTag() error: %v", err)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != testComposeContent {
		t.Error("file should be unchanged when new image has no tag")
	}
}

func TestUpdateComposeTag_FileNotFound(t *testing.T) {
	err := UpdateComposeTag("/nonexistent/path/docker-compose.yml", "nginx", "nginx:2.0")
	if err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}
}

func TestUpdateComposeTag_SecondService(t *testing.T) {
	p := writeComposeFile(t, testComposeContent)

	err := UpdateComposeTag(p, "redis", "redis:8")
	if err != nil {
		t.Fatalf("UpdateComposeTag() error: %v", err)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	content := string(data)
	if !containsString(content, "image: redis:8") {
		t.Errorf("expected redis tag updated to 8, got:\n%s", content)
	}
	// nginx should be unchanged.
	if !containsString(content, "image: nginx:1.24") {
		t.Errorf("nginx tag should be unchanged, got:\n%s", content)
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
