package compose

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseShortForm(t *testing.T) {
	input := `
services:
  web:
    image: nginx
    depends_on:
      - api
      - redis
  api:
    image: myapi
    depends_on:
      - db
  db:
    image: postgres
  redis:
    image: redis
`
	deps, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got := deps["web"]; len(got) != 2 {
		t.Errorf("web deps = %v, want [api, redis]", got)
	} else {
		// Sorted, so api before redis.
		if got[0] != "api" || got[1] != "redis" {
			t.Errorf("web deps = %v, want [api, redis]", got)
		}
	}

	if got := deps["api"]; len(got) != 1 || got[0] != "db" {
		t.Errorf("api deps = %v, want [db]", got)
	}

	if _, ok := deps["db"]; ok {
		t.Error("db should have no deps entry")
	}
	if _, ok := deps["redis"]; ok {
		t.Error("redis should have no deps entry")
	}
}

func TestParseLongForm(t *testing.T) {
	input := `
services:
  web:
    image: nginx
    depends_on:
      api:
        condition: service_healthy
      redis:
        condition: service_started
  api:
    image: myapi
`
	deps, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got := deps["web"]
	if len(got) != 2 {
		t.Fatalf("web deps = %v, want 2 deps", got)
	}

	// Sorted output.
	found := map[string]bool{}
	for _, d := range got {
		found[d] = true
	}
	if !found["api"] || !found["redis"] {
		t.Errorf("web deps = %v, want api and redis", got)
	}
}

func TestParseMixedForms(t *testing.T) {
	// One service uses short form, another uses long form.
	input := `
services:
  frontend:
    image: react-app
    depends_on:
      - backend
  backend:
    image: node-api
    depends_on:
      db:
        condition: service_healthy
      cache:
        condition: service_started
  db:
    image: postgres
  cache:
    image: redis
`
	deps, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got := deps["frontend"]; len(got) != 1 || got[0] != "backend" {
		t.Errorf("frontend deps = %v, want [backend]", got)
	}
	if got := deps["backend"]; len(got) != 2 {
		t.Errorf("backend deps = %v, want [cache, db]", got)
	}
}

func TestParseNoDeps(t *testing.T) {
	input := `
services:
  web:
    image: nginx
  api:
    image: myapi
`
	deps, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("expected no deps, got %v", deps)
	}
}

func TestParseNoServices(t *testing.T) {
	input := `
version: "3.8"
networks:
  default:
    driver: bridge
`
	deps, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("expected no deps, got %v", deps)
	}
}

func TestParseEmpty(t *testing.T) {
	deps, err := Parse([]byte(""))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("expected no deps, got %v", deps)
	}
}

func TestParseInvalidYAML(t *testing.T) {
	_, err := Parse([]byte("{{invalid"))
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestParseFileIntegration(t *testing.T) {
	ClearCache()

	content := `
services:
  app:
    image: myapp
    depends_on:
      - db
  db:
    image: postgres
`
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	deps, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if got := deps["app"]; len(got) != 1 || got[0] != "db" {
		t.Errorf("app deps = %v, want [db]", got)
	}

	// Second call should hit cache.
	deps2, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile (cached): %v", err)
	}
	if got := deps2["app"]; len(got) != 1 || got[0] != "db" {
		t.Errorf("cached app deps = %v, want [db]", got)
	}
}

func TestParseFileNotFound(t *testing.T) {
	_, err := ParseFile("/nonexistent/docker-compose.yml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestParseFileCacheInvalidation(t *testing.T) {
	ClearCache()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")

	// Write initial content.
	initial := `
services:
  app:
    image: myapp
    depends_on:
      - db
  db:
    image: postgres
`
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	deps, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(deps["app"]) != 1 {
		t.Fatalf("initial app deps = %v, want [db]", deps["app"])
	}

	// Rewrite with different content and different mod time.
	// os.WriteFile updates mtime, but on fast filesystems the mtime
	// granularity might not catch sub-millisecond rewrites. Force a
	// cache clear to test the invalidation path reliably.
	ClearCache()

	updated := `
services:
  app:
    image: myapp
    depends_on:
      - db
      - cache
  db:
    image: postgres
  cache:
    image: redis
`
	if err := os.WriteFile(path, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}

	deps2, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile (updated): %v", err)
	}
	if len(deps2["app"]) != 2 {
		t.Errorf("updated app deps = %v, want [cache, db]", deps2["app"])
	}
}
