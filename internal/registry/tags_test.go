package registry

import (
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
)

func TestParseSemVerValid(t *testing.T) {
	tests := []struct {
		tag   string
		major int
		minor int
		patch int
		pre   string
	}{
		{"1.2.3", 1, 2, 3, ""},
		{"v1.2.3", 1, 2, 3, ""},
		{"V1.2.3", 1, 2, 3, ""},
		{"1.25", 1, 25, 0, ""},
		{"v2.0", 2, 0, 0, ""},
		{"1.0.0-rc1", 1, 0, 0, "rc1"},
		{"v3.2.1-beta2", 3, 2, 1, "beta2"},
		{"0.1.0", 0, 1, 0, ""},
		{"10.20.30", 10, 20, 30, ""},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			sv, ok := ParseSemVer(tt.tag)
			if !ok {
				t.Fatalf("ParseSemVer(%q) returned false, want true", tt.tag)
			}
			if sv.Major != tt.major {
				t.Errorf("Major = %d, want %d", sv.Major, tt.major)
			}
			if sv.Minor != tt.minor {
				t.Errorf("Minor = %d, want %d", sv.Minor, tt.minor)
			}
			if sv.Patch != tt.patch {
				t.Errorf("Patch = %d, want %d", sv.Patch, tt.patch)
			}
			if sv.Pre != tt.pre {
				t.Errorf("Pre = %q, want %q", sv.Pre, tt.pre)
			}
			if sv.Raw != tt.tag {
				t.Errorf("Raw = %q, want %q", sv.Raw, tt.tag)
			}
		})
	}
}

func TestParseSemVerInvalid(t *testing.T) {
	invalid := []string{
		"latest",
		"stable",
		"alpine",
		"1",
		"v",
		"",
		"abc.def.ghi",
		"1.2.3.4",
		"not-a-version",
	}

	for _, tag := range invalid {
		t.Run(tag, func(t *testing.T) {
			_, ok := ParseSemVer(tag)
			if ok {
				t.Errorf("ParseSemVer(%q) returned true, want false", tag)
			}
		})
	}
}

func TestLessThan(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"major less", "1.0.0", "2.0.0", true},
		{"major greater", "2.0.0", "1.0.0", false},
		{"minor less", "1.1.0", "1.2.0", true},
		{"minor greater", "1.2.0", "1.1.0", false},
		{"patch less", "1.2.3", "1.2.4", true},
		{"patch greater", "1.2.4", "1.2.3", false},
		{"equal", "1.2.3", "1.2.3", false},
		{"pre less than release", "1.2.3-rc1", "1.2.3", true},
		{"release greater than pre", "1.2.3", "1.2.3-rc1", false},
		{"pre alphabetical", "1.2.3-alpha", "1.2.3-beta", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, ok := ParseSemVer(tt.a)
			if !ok {
				t.Fatalf("failed to parse %q", tt.a)
			}
			b, ok := ParseSemVer(tt.b)
			if !ok {
				t.Fatalf("failed to parse %q", tt.b)
			}
			if got := a.LessThan(b); got != tt.want {
				t.Errorf("(%q).LessThan(%q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestNewerVersions(t *testing.T) {
	// Use a 2-part current tag so cross-minor results are in scope.
	tags := []string{
		"1.23.0", "1.24.0", "1.25.0", "1.25.1", "1.26.0",
		"latest", "alpine", "1.25.0-rc1",
		"v1.27.0", "1.20.0",
	}

	newer := NewerVersions("1.25", tags)

	if len(newer) != 3 {
		t.Fatalf("NewerVersions returned %d items, want 3 (v1.27.0, 1.26.0, 1.25.1)", len(newer))
	}

	// Should be sorted newest first.
	expected := []string{"v1.27.0", "1.26.0", "1.25.1"}
	for i, sv := range newer {
		if sv.Raw != expected[i] {
			t.Errorf("newer[%d].Raw = %q, want %q", i, sv.Raw, expected[i])
		}
	}
}

func TestNewerVersionsNonSemverCurrent(t *testing.T) {
	newer := NewerVersions("latest", []string{"1.0.0", "2.0.0"})
	if newer != nil {
		t.Errorf("expected nil for non-semver current, got %v", newer)
	}
}

func TestNewerVersionsNoNewer(t *testing.T) {
	tags := []string{"1.0.0", "1.1.0", "1.2.0"}
	newer := NewerVersions("2.0.0", tags)
	if len(newer) != 0 {
		t.Errorf("expected no newer versions, got %d", len(newer))
	}
}

func TestNewerVersionsCalverMismatch(t *testing.T) {
	// linuxserver images have both semver (3.21) and calver (2021.12.14) tags.
	// Calver tags should NOT appear as "newer" than semver tags.
	tags := []string{"3.20", "3.22", "2021.12.14", "2021.11.27", "2022.01.05"}
	newer := NewerVersions("3.21", tags)
	if len(newer) != 1 {
		t.Fatalf("expected 1 newer version, got %d: %v", len(newer), newer)
	}
	if newer[0].Raw != "3.22" {
		t.Errorf("expected 3.22, got %s", newer[0].Raw)
	}
}

func TestNewerVersionsCalverToCalver(t *testing.T) {
	// When both current and candidates are calver, comparison should work.
	tags := []string{"2021.11.27", "2022.01.05", "2020.06.01"}
	newer := NewerVersions("2021.12.14", tags)
	if len(newer) != 1 {
		t.Fatalf("expected 1 newer version, got %d: %v", len(newer), newer)
	}
	if newer[0].Raw != "2022.01.05" {
		t.Errorf("expected 2022.01.05, got %s", newer[0].Raw)
	}
}

func TestNewerVersionsThreePartScope(t *testing.T) {
	// 3-part tag: only same major.minor (patch updates).
	tags := []string{"1.13.4", "1.13.5", "1.14.0", "1.15.0", "2.0.0", "1.12.0"}
	newer := NewerVersions("v1.13.3", tags)

	expected := []string{"1.13.5", "1.13.4"}
	if len(newer) != len(expected) {
		t.Fatalf("got %d versions, want %d: %v", len(newer), len(expected), newer)
	}
	for i, sv := range newer {
		if sv.Raw != expected[i] {
			t.Errorf("newer[%d] = %q, want %q", i, sv.Raw, expected[i])
		}
	}
}

func TestNewerVersionsTwoPartScope(t *testing.T) {
	// 2-part tag: same major (minor+patch updates allowed).
	tags := []string{"3.20", "3.22", "3.23.1", "4.0.0", "3.19"}
	newer := NewerVersions("3.21", tags)

	expected := []string{"3.23.1", "3.22"}
	if len(newer) != len(expected) {
		t.Fatalf("got %d versions, want %d: %v", len(newer), len(expected), newer)
	}
	for i, sv := range newer {
		if sv.Raw != expected[i] {
			t.Errorf("newer[%d] = %q, want %q", i, sv.Raw, expected[i])
		}
	}
}

func TestNewerVersionsScoped(t *testing.T) {
	tags := []string{"1.13.4", "1.13.5", "1.14.0", "1.15.0", "2.0.0", "1.12.0"}

	tests := []struct {
		name     string
		current  string
		scope    docker.SemverScope
		expected []string
	}{
		{"3part_default_patch_only", "v1.13.3", docker.ScopeDefault, []string{"1.13.5", "1.13.4"}},
		{"3part_patch_same_as_default", "v1.13.3", docker.ScopePatch, []string{"1.13.5", "1.13.4"}},
		{"3part_minor_widens", "v1.13.3", docker.ScopeMinor, []string{"1.15.0", "1.14.0", "1.13.5", "1.13.4"}},
		{"3part_major_all", "v1.13.3", docker.ScopeMajor, []string{"2.0.0", "1.15.0", "1.14.0", "1.13.5", "1.13.4"}},
		// 2-part current: default is same-major; patch narrows to same major.minor
		{"2part_default_same_major", "1.13", docker.ScopeDefault, []string{"1.15.0", "1.14.0", "1.13.4", "1.13.5"}},
		{"2part_patch_narrows", "1.13", docker.ScopePatch, []string{"1.13.4", "1.13.5"}},
		{"2part_minor_same_as_default", "1.13", docker.ScopeMinor, []string{"1.15.0", "1.14.0", "1.13.4", "1.13.5"}},
		{"2part_major_all", "1.13", docker.ScopeMajor, []string{"2.0.0", "1.15.0", "1.14.0", "1.13.4", "1.13.5"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewerVersionsScoped(tt.current, tags, tt.scope)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d versions, want %d: %v", len(got), len(tt.expected), got)
			}
			gotSet := make(map[string]bool, len(got))
			for _, sv := range got {
				gotSet[sv.Raw] = true
			}
			for _, exp := range tt.expected {
				if !gotSet[exp] {
					t.Errorf("expected %q in results, got %v", exp, got)
				}
			}
		})
	}
}

func TestNormaliseRepo(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"nginx", "library/nginx"},
		{"nginx:1.25", "library/nginx"},
		{"gitea/gitea", "gitea/gitea"},
		{"gitea/gitea:1.21", "gitea/gitea"},
		{"nginx@sha256:abc", "library/nginx"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormaliseRepo(tt.input)
			if got != tt.want {
				t.Errorf("NormaliseRepo(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractTag(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"nginx:1.25", "1.25"},
		{"library/nginx:latest", "latest"},
		{"nginx", ""},
		{"nginx@sha256:abc123", ""},
		{"ghcr.io/owner/app:v1.2.3", "v1.2.3"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ExtractTag(tt.input)
			if got != tt.want {
				t.Errorf("ExtractTag(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
