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
		parts int
	}{
		{"1.2.3", 1, 2, 3, "", 3},
		{"v1.2.3", 1, 2, 3, "", 3},
		{"V1.2.3", 1, 2, 3, "", 3},
		{"1.25", 1, 25, 0, "", 2},
		{"v2.0", 2, 0, 0, "", 2},
		{"1.0.0-rc1", 1, 0, 0, "rc1", 3},
		{"v3.2.1-beta2", 3, 2, 1, "beta2", 3},
		{"0.1.0", 0, 1, 0, "", 3},
		{"10.20.30", 10, 20, 30, "", 3},
		// 1-part tags
		{"1", 1, 0, 0, "", 1},
		{"v2", 2, 0, 0, "", 1},
		{"3-rc1", 3, 0, 0, "rc1", 1},
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
			if sv.Parts != tt.parts {
				t.Errorf("Parts = %d, want %d", sv.Parts, tt.parts)
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

	// NEW-1: same-subtree (1.25.x) is skipped because cur=1.25 is a floating
	// 2-part pointer that already tracks its descendants by digest. Only
	// cross-minor candidates remain.
	if len(newer) != 2 {
		t.Fatalf("NewerVersions returned %d items, want 2 (v1.27.0, 1.26.0): %v", len(newer), rawVersions(newer))
	}

	// Should be sorted newest first.
	expected := []string{"v1.27.0", "1.26.0"}
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
		{"2part_default_same_major", "1.13", docker.ScopeDefault, []string{"1.15.0", "1.14.0"}},
		{"2part_patch_narrows", "1.13", docker.ScopePatch, nil},
		{"2part_minor_same_as_default", "1.13", docker.ScopeMinor, []string{"1.15.0", "1.14.0"}},
		{"2part_major_all", "1.13", docker.ScopeMajor, []string{"2.0.0", "1.15.0", "1.14.0"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewerVersionsScoped(tt.current, tags, tt.scope, docker.ScopeDefault)
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

func TestNewerVersionsScopedStrict(t *testing.T) {
	tags := []string{"1.13.4", "1.13.5", "1.14.0", "1.15.0", "2.0.0", "1.12.0"}

	tests := []struct {
		name     string
		current  string
		scope    docker.SemverScope
		defScope docker.SemverScope
		expected []string
	}{
		// Strict mode tests.
		{"strict_3part_no_updates", "v1.13.3", docker.ScopeDefault, docker.ScopeStrict, nil},
		{"strict_2part_patch_only", "1.13", docker.ScopeDefault, docker.ScopeStrict, nil},
		// Per-container scope overrides global strict.
		{"strict_override_minor", "v1.13.3", docker.ScopeMinor, docker.ScopeStrict, []string{"1.15.0", "1.14.0", "1.13.5", "1.13.4"}},
		{"strict_override_major", "v1.13.3", docker.ScopeMajor, docker.ScopeStrict, []string{"2.0.0", "1.15.0", "1.14.0", "1.13.5", "1.13.4"}},
		// Default (relaxed) unchanged.
		{"relaxed_2part_same_major", "1.13", docker.ScopeDefault, docker.ScopeDefault, []string{"1.15.0", "1.14.0"}},
		{"relaxed_3part_patch_only", "v1.13.3", docker.ScopeDefault, docker.ScopeDefault, []string{"1.13.5", "1.13.4"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewerVersionsScoped(tt.current, tags, tt.scope, tt.defScope)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d versions, want %d: %v", len(got), len(tt.expected), rawVersions(got))
			}
			gotSet := make(map[string]bool, len(got))
			for _, sv := range got {
				gotSet[sv.Raw] = true
			}
			for _, exp := range tt.expected {
				if !gotSet[exp] {
					t.Errorf("expected %q in results, got %v", exp, rawVersions(got))
				}
			}
		})
	}
}

func TestNewerVersionsStrictOnePart(t *testing.T) {
	tags := []string{"1.0.0", "1.1.0", "1.2.0", "2.0.0", "2.1.0", "3.0.0"}

	// NEW-1: cur=1 (Parts=1) is a floating major-version pointer; same-major
	// candidates (1.x.x) are skipped because the floating tag tracks them by
	// digest. Strict mode further restricts to same-major, leaving nothing.
	got := NewerVersionsScoped("1", tags, docker.ScopeDefault, docker.ScopeStrict)
	if len(got) != 0 {
		t.Fatalf("strict 1-part: got %d versions, want 0: %v", len(got), rawVersions(got))
	}

	// Relaxed + 1-part tag: same-major skipped by NEW-1; cross-major candidates
	// (2.0.0, 2.1.0, 3.0.0) remain.
	got = NewerVersionsScoped("1", tags, docker.ScopeDefault, docker.ScopeDefault)
	expected := []string{"3.0.0", "2.1.0", "2.0.0"}
	if len(got) != len(expected) {
		t.Fatalf("relaxed 1-part: got %d versions, want %d: %v", len(got), len(expected), rawVersions(got))
	}
	gotSet := make(map[string]bool, len(got))
	for _, sv := range got {
		gotSet[sv.Raw] = true
	}
	for _, exp := range expected {
		if !gotSet[exp] {
			t.Errorf("expected %q in results, got %v", exp, rawVersions(got))
		}
	}
}

func rawVersions(svs []SemVer) []string {
	out := make([]string, len(svs))
	for i, sv := range svs {
		out[i] = sv.Raw
	}
	return out
}

// TestNewerVersionsScoped_FloatingTagAndDedup covers the NEW-1 (same-subtree skip)
// and NEW-2 (dedup equivalent versions) fixes from the 2026-05-08 #82 fix-batch.
func TestNewerVersionsScoped_FloatingTagAndDedup(t *testing.T) {
	tests := []struct {
		name     string
		current  string
		tags     []string
		expected []string
	}{
		{
			name:     "NEW-1_calver_floating_2part",
			current:  "2026.4",
			tags:     []string{"2026.4", "2026.4.0", "2026.4.4", "2026.5", "2026.5.1"},
			expected: []string{"2026.5.1", "2026.5"},
		},
		{
			name:     "NEW-1_semver_floating_1part",
			current:  "1",
			tags:     []string{"1", "1.25.0", "1.25.5", "2.0.0"},
			expected: []string{"2.0.0"},
		},
		{
			name:     "NEW-1_boundary_2part_equal_3part_zero",
			current:  "2026.4",
			tags:     []string{"2026.4.0"},
			expected: nil,
		},
		{
			name:     "NEW-2_dedup_calver_2_and_3_part",
			current:  "2026.3",
			tags:     []string{"2026.4", "2026.4.0", "2026.5.0", "2026.5"},
			expected: []string{"2026.5.0", "2026.4.0"},
		},
		{
			name:     "NEW-2_pre_release_retained_distinct",
			current:  "2026.4.0",
			tags:     []string{"2026.4.1", "2026.4.1-rc1"},
			expected: []string{"2026.4.1", "2026.4.1-rc1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewerVersionsScoped(tt.current, tt.tags, docker.ScopeDefault, docker.ScopeDefault)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d versions, want %d: %v", len(got), len(tt.expected), rawVersions(got))
			}
			gotSet := make(map[string]bool, len(got))
			for _, sv := range got {
				gotSet[sv.Raw] = true
			}
			for _, exp := range tt.expected {
				if !gotSet[exp] {
					t.Errorf("expected %q in results, got %v", exp, rawVersions(got))
				}
			}
		})
	}
}

func TestFilterTags(t *testing.T) {
	tags := []string{"1.0.0", "1.0.1", "1.1.0", "2.0.0-rc1", "2.0.0", "latest", "alpine"}

	tests := []struct {
		name    string
		include string
		exclude string
		want    []string
	}{
		{"no filters", "", "", tags},
		{"include only", `^\d+\.\d+\.\d+$`, "", []string{"1.0.0", "1.0.1", "1.1.0", "2.0.0"}},
		{"exclude rc", "", `rc`, []string{"1.0.0", "1.0.1", "1.1.0", "2.0.0", "latest", "alpine"}},
		{"include and exclude", `^\d`, `rc`, []string{"1.0.0", "1.0.1", "1.1.0", "2.0.0"}},
		{"invalid include regex ignored", `[invalid`, "", tags},
		{"empty result", `^nonexistent$`, "", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterTags(tags, tt.include, tt.exclude)
			if len(got) != len(tt.want) {
				t.Fatalf("FilterTags len = %d, want %d: got %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("FilterTags[%d] = %q, want %q", i, got[i], tt.want[i])
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

// TestNewerVersionsScopedWithBeyond covers the #83 scope-hint helper: it returns
// the scoped newer-versions list plus a count of higher registry versions that
// exist beyond the effective scope. The count is the ScopeMajor length minus the
// scoped length, which isolates exactly the scope-switch effect.
func TestNewerVersionsScopedWithBeyond(t *testing.T) {
	tests := []struct {
		name         string
		current      string
		tags         []string
		scope        docker.SemverScope
		defaultScope docker.SemverScope
		wantScoped   []string
		wantBeyond   int
	}{
		{
			// gallery-server: relaxed 3-part scope keeps only same-minor patches,
			// of which there are none, but three higher minors exist on the registry.
			name:         "gallery_server_relaxed_empty_beyond_3",
			current:      "v4.50.0",
			tags:         []string{"v4.50.0", "v4.51.0", "v4.52.0", "v4.55.2"},
			scope:        docker.ScopeDefault,
			defaultScope: docker.ScopeDefault,
			wantScoped:   nil,
			wantBeyond:   3,
		},
		{
			// Normal case: one same-minor patch is in scope, one higher minor is beyond.
			name:         "normal_relaxed_one_scoped_one_beyond",
			current:      "1.2.0",
			tags:         []string{"1.2.0", "1.2.1", "1.3.0"},
			scope:        docker.ScopeDefault,
			defaultScope: docker.ScopeDefault,
			wantScoped:   []string{"1.2.1"},
			wantBeyond:   1,
		},
		{
			// Non-semver current tag: ParseSemVer fails, both calls empty, no hint.
			name:         "latest_non_semver_zero",
			current:      "latest",
			tags:         []string{"v4.51.0", "v4.55.2"},
			scope:        docker.ScopeDefault,
			defaultScope: docker.ScopeDefault,
			wantScoped:   nil,
			wantBeyond:   0,
		},
		{
			// Calver current tag is scope-exempt, so widening to ScopeMajor adds
			// nothing the scoped call did not already include: no spurious hint.
			name:         "calver_scope_exempt_zero",
			current:      "2026.4",
			tags:         []string{"2026.4", "2026.5", "2026.5.1"},
			scope:        docker.ScopeDefault,
			defaultScope: docker.ScopeDefault,
			wantScoped:   []string{"2026.5.1", "2026.5"},
			wantBeyond:   0,
		},
		{
			// Already newest: nothing higher anywhere, beyond is zero.
			name:         "already_newest_zero",
			current:      "v4.55.2",
			tags:         []string{"v4.50.0", "v4.51.0", "v4.55.2"},
			scope:        docker.ScopeDefault,
			defaultScope: docker.ScopeDefault,
			wantScoped:   nil,
			wantBeyond:   0,
		},
		{
			// Strict global default: 3-part current allows no updates at all under
			// the effective (strict) scope, yet higher patch+minor+major exist beyond.
			name:         "strict_default_3part_empty_beyond",
			current:      "1.13.3",
			tags:         []string{"1.13.3", "1.13.4", "1.14.0", "2.0.0"},
			scope:        docker.ScopeDefault,
			defaultScope: docker.ScopeStrict,
			wantScoped:   nil,
			wantBeyond:   3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotScoped, gotBeyond := NewerVersionsScopedWithBeyond(tt.current, tt.tags, tt.scope, tt.defaultScope)
			if len(gotScoped) != len(tt.wantScoped) {
				t.Fatalf("scoped: got %d versions, want %d: %v", len(gotScoped), len(tt.wantScoped), rawVersions(gotScoped))
			}
			gotSet := make(map[string]bool, len(gotScoped))
			for _, sv := range gotScoped {
				gotSet[sv.Raw] = true
			}
			for _, exp := range tt.wantScoped {
				if !gotSet[exp] {
					t.Errorf("expected %q in scoped results, got %v", exp, rawVersions(gotScoped))
				}
			}
			if gotBeyond != tt.wantBeyond {
				t.Errorf("beyondScope = %d, want %d", gotBeyond, tt.wantBeyond)
			}
		})
	}
}

func TestSemVerVariant(t *testing.T) {
	tests := []struct {
		tag  string
		want string
	}{
		{"1.24.0", ""},
		{"v2.0.0", ""},
		// Pre-release suffixes are ordering qualifiers, not variants.
		{"1.0.0-rc1", ""},
		{"1.0.0-rc.2", ""},
		{"1.0.0-beta2", ""},
		{"1.0.0-alpha", ""},
		{"1.0.0-dev", ""},
		{"1.0.0-snapshot", ""},
		{"1.0.0-preview1", ""},
		// Build variants partition the tag space.
		{"1.24.0-alpine", "alpine"},
		{"1.24.0-alpine3.19", "alpine3.19"},
		{"1.24.0-perl", "perl"},
		{"1.24.0-bookworm", "bookworm"},
		{"1.24.0-otel", "otel"},
		{"1.24.0-alpine-perl", "alpine-perl"},
		{"1.24.0-slim", "slim"},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			sv, ok := ParseSemVer(tt.tag)
			if !ok {
				t.Fatalf("ParseSemVer(%q) returned false, want true", tt.tag)
			}
			if got := sv.Variant(); got != tt.want {
				t.Errorf("Variant() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNewerVersionsScoped_VariantAware covers #84: variant-suffixed tags must
// not appear as newer versions for a differently-flavoured current tag.
func TestNewerVersionsScoped_VariantAware(t *testing.T) {
	// nginx-style tag zoo: each version line published in several flavours.
	nginxTags := []string{
		"1.24.0", "1.24.0-alpine", "1.24.0-perl", "1.24.0-bookworm", "1.24.0-otel",
		"1.25.1", "1.25.1-alpine", "1.25.1-perl", "1.25.1-bookworm", "1.25.1-otel",
		"1.25.2", "1.25.2-alpine", "1.25.2-perl", "1.25.2-bookworm", "1.25.2-otel",
	}

	tests := []struct {
		name     string
		current  string
		tags     []string
		scope    docker.SemverScope
		expected []string
	}{
		{
			// Bare current: only bare tags count, flavours are invisible.
			name:     "bare_current_ignores_variants",
			current:  "1.25.1",
			tags:     nginxTags,
			scope:    docker.ScopeDefault,
			expected: []string{"1.25.2"},
		},
		{
			// Variant current: only the same flavour counts.
			name:     "alpine_current_sees_only_alpine",
			current:  "1.25.1-alpine",
			tags:     nginxTags,
			scope:    docker.ScopeDefault,
			expected: []string{"1.25.2-alpine"},
		},
		{
			// Different variant present but no matching newer tag: empty, not
			// another flavour's release.
			name:     "perl_current_no_cross_variant_leak",
			current:  "1.25.2-perl",
			tags:     nginxTags,
			scope:    docker.ScopeMajor,
			expected: nil,
		},
		{
			// Pre-release current still sees its release counterpart and later
			// pre-releases: rc/beta/alpha are ordering qualifiers, not variants.
			name:     "prerelease_current_sees_release",
			current:  "2.0.0-rc1",
			tags:     []string{"2.0.0-rc1", "2.0.0-rc2", "2.0.0", "2.0.0-alpine"},
			scope:    docker.ScopePatch,
			expected: []string{"2.0.0-rc2", "2.0.0"},
		},
		{
			// Bare current sees pre-releases of higher versions but no variants.
			name:     "bare_current_sees_higher_prerelease",
			current:  "1.25.2",
			tags:     []string{"1.25.3-rc1", "1.25.3-alpine", "1.26.0-beta1"},
			scope:    docker.ScopeMajor,
			expected: []string{"1.26.0-beta1", "1.25.3-rc1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewerVersionsScoped(tt.current, tt.tags, tt.scope, docker.ScopeDefault)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d versions, want %d: %v", len(got), len(tt.expected), rawVersions(got))
			}
			gotSet := make(map[string]bool, len(got))
			for _, sv := range got {
				gotSet[sv.Raw] = true
			}
			for _, exp := range tt.expected {
				if !gotSet[exp] {
					t.Errorf("expected %q in results, got %v", exp, rawVersions(got))
				}
			}
		})
	}
}

// TestNewerVersionsScopedWithBeyond_VariantAware covers the #84 headline case:
// the beyond-scope count must reflect distinct same-variant version lines, not
// every flavour multiplied out (nginx:1.24.0 previously reported hundreds).
func TestNewerVersionsScopedWithBeyond_VariantAware(t *testing.T) {
	variants := []string{"", "-alpine", "-perl", "-bookworm", "-otel", "-alpine-perl", "-alpine-slim", "-otel-alpine"}
	versions := []string{"1.25.0", "1.25.1", "1.26.0", "1.26.1", "1.27.0"}
	var tags []string
	for _, v := range versions {
		for _, suffix := range variants {
			tags = append(tags, v+suffix)
		}
	}

	// Bare 1.24.0 under relaxed default scope: nothing in scope (no newer
	// 1.24.x), and beyond-scope counts the 5 bare higher versions only --
	// not 5 versions x 8 flavours = 40.
	scoped, beyond := NewerVersionsScopedWithBeyond("1.24.0", tags, docker.ScopeDefault, docker.ScopeDefault)
	if len(scoped) != 0 {
		t.Fatalf("scoped: got %d versions, want 0: %v", len(scoped), rawVersions(scoped))
	}
	if beyond != len(versions) {
		t.Errorf("beyondScope = %d, want %d (bare version lines only)", beyond, len(versions))
	}

	// Same shape for a variant-pinned current: only -alpine lines count.
	scoped, beyond = NewerVersionsScopedWithBeyond("1.24.0-alpine", tags, docker.ScopeDefault, docker.ScopeDefault)
	if len(scoped) != 0 {
		t.Fatalf("alpine scoped: got %d versions, want 0: %v", len(scoped), rawVersions(scoped))
	}
	if beyond != len(versions) {
		t.Errorf("alpine beyondScope = %d, want %d (alpine version lines only)", beyond, len(versions))
	}
}
