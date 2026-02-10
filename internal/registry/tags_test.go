package registry

import "testing"

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
	tags := []string{
		"1.23.0", "1.24.0", "1.25.0", "1.25.1", "1.26.0",
		"latest", "alpine", "1.25.0-rc1",
		"v1.27.0", "1.20.0",
	}

	newer := NewerVersions("1.25.0", tags)

	if len(newer) != 3 {
		t.Fatalf("NewerVersions returned %d items, want 3 (1.25.1, 1.26.0, v1.27.0)", len(newer))
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
			got := normaliseRepo(tt.input)
			if got != tt.want {
				t.Errorf("normaliseRepo(%q) = %q, want %q", tt.input, got, tt.want)
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
			got := extractTag(tt.input)
			if got != tt.want {
				t.Errorf("extractTag(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
