package compose

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverPaths(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   int
	}{
		{"single path", map[string]string{ConfigFilesLabel: "/opt/docker/app/docker-compose.yml"}, 1},
		{"multiple paths", map[string]string{ConfigFilesLabel: "/a.yml,/b.yml"}, 2},
		{"with spaces", map[string]string{ConfigFilesLabel: " /a.yml , /b.yml "}, 2},
		{"empty value", map[string]string{ConfigFilesLabel: ""}, 0},
		{"no label", map[string]string{"other": "value"}, 0},
		{"empty labels", map[string]string{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := DiscoverPaths(tt.labels)
			if len(paths) != tt.want {
				t.Errorf("got %d paths, want %d: %v", len(paths), tt.want, paths)
			}
		})
	}
}

func TestServiceName(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"present", map[string]string{ServiceLabel: "web"}, "web"},
		{"missing", map[string]string{}, ""},
		{"empty", map[string]string{ServiceLabel: ""}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ServiceName(tt.labels); got != tt.want {
				t.Errorf("ServiceName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProjectName(t *testing.T) {
	labels := map[string]string{ProjectLabel: "myproject"}
	if got := ProjectName(labels); got != "myproject" {
		t.Errorf("ProjectName = %q, want myproject", got)
	}

	if got := ProjectName(map[string]string{}); got != "" {
		t.Errorf("ProjectName = %q, want empty", got)
	}
}

func TestDiscoverDeps(t *testing.T) {
	ClearCache()

	// Write a compose file to disk.
	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	content := `
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
	if err := os.WriteFile(composePath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		labels map[string]string
		want   int
	}{
		{
			"web service with deps",
			map[string]string{
				ServiceLabel:     "web",
				ConfigFilesLabel: composePath,
			},
			2, // api, redis
		},
		{
			"api service with deps",
			map[string]string{
				ServiceLabel:     "api",
				ConfigFilesLabel: composePath,
			},
			1, // db
		},
		{
			"db service no deps",
			map[string]string{
				ServiceLabel:     "db",
				ConfigFilesLabel: composePath,
			},
			0,
		},
		{
			"no service label",
			map[string]string{
				ConfigFilesLabel: composePath,
			},
			0,
		},
		{
			"no config label",
			map[string]string{
				ServiceLabel: "web",
			},
			0,
		},
		{
			"nonexistent compose file",
			map[string]string{
				ServiceLabel:     "web",
				ConfigFilesLabel: "/nonexistent/docker-compose.yml",
			},
			0, // graceful fallback
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := DiscoverDeps(tt.labels)
			if len(deps) != tt.want {
				t.Errorf("got %d deps, want %d: %v", len(deps), tt.want, deps)
			}
		})
	}
}

func TestDiscoverDepsMultipleFiles(t *testing.T) {
	ClearCache()

	dir := t.TempDir()

	// Base compose file.
	base := filepath.Join(dir, "docker-compose.yml")
	baseContent := `
services:
  web:
    image: nginx
    depends_on:
      - api
  api:
    image: myapi
`
	if err := os.WriteFile(base, []byte(baseContent), 0600); err != nil {
		t.Fatal(err)
	}

	// Override file adds another dependency.
	override := filepath.Join(dir, "docker-compose.override.yml")
	overrideContent := `
services:
  web:
    depends_on:
      - cache
  cache:
    image: redis
`
	if err := os.WriteFile(override, []byte(overrideContent), 0600); err != nil {
		t.Fatal(err)
	}

	labels := map[string]string{
		ServiceLabel:     "web",
		ConfigFilesLabel: base + "," + override,
	}

	deps := DiscoverDeps(labels)
	// Should have api (from base) + cache (from override) = 2 unique deps.
	if len(deps) != 2 {
		t.Errorf("got %d deps, want 2: %v", len(deps), deps)
	}

	found := map[string]bool{}
	for _, d := range deps {
		found[d] = true
	}
	if !found["api"] || !found["cache"] {
		t.Errorf("deps = %v, want api and cache", deps)
	}
}

func TestMergeDeps(t *testing.T) {
	compose := ServiceDeps{
		"web": {"api", "redis"},
		"api": {"db"},
	}
	labels := ServiceDeps{
		"web": {"api"}, // Label overrides compose (removes redis dep).
	}

	merged := MergeDeps(labels, compose)

	// "web" should have label version only (just api).
	if got := merged["web"]; len(got) != 1 || got[0] != "api" {
		t.Errorf("web deps = %v, want [api]", got)
	}

	// "api" should keep compose version.
	if got := merged["api"]; len(got) != 1 || got[0] != "db" {
		t.Errorf("api deps = %v, want [db]", got)
	}
}

func TestMergeDepsNoOverlap(t *testing.T) {
	compose := ServiceDeps{
		"api": {"db"},
	}
	labels := ServiceDeps{
		"web": {"proxy"},
	}

	merged := MergeDeps(labels, compose)

	if got := merged["api"]; len(got) != 1 || got[0] != "db" {
		t.Errorf("api deps = %v, want [db]", got)
	}
	if got := merged["web"]; len(got) != 1 || got[0] != "proxy" {
		t.Errorf("web deps = %v, want [proxy]", got)
	}
}

func TestMergeDepsEmpty(t *testing.T) {
	merged := MergeDeps(ServiceDeps{}, ServiceDeps{})
	if len(merged) != 0 {
		t.Errorf("expected empty merge, got %v", merged)
	}
}
