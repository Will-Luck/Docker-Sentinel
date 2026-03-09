package store

import "testing"

// ---------------------------------------------------------------------------
// Registry: Ignored Versions
// ---------------------------------------------------------------------------

func TestIgnoredVersionsRoundTrip(t *testing.T) {
	s := testStore(t)

	versions, err := s.GetIgnoredVersions("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 0 {
		t.Errorf("expected empty slice, got %v", versions)
	}

	if err := s.AddIgnoredVersion("nginx", "1.24"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddIgnoredVersion("nginx", "1.25"); err != nil {
		t.Fatal(err)
	}

	versions, err = s.GetIgnoredVersions("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions))
	}
	if versions[0] != "1.24" || versions[1] != "1.25" {
		t.Errorf("versions = %v, want [1.24, 1.25]", versions)
	}
}

func TestIgnoredVersionsDeduplication(t *testing.T) {
	s := testStore(t)

	if err := s.AddIgnoredVersion("app", "v2.0"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddIgnoredVersion("app", "v2.0"); err != nil {
		t.Fatal(err)
	}

	versions, err := s.GetIgnoredVersions("app")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Errorf("expected 1 (deduplicated), got %d: %v", len(versions), versions)
	}
}

func TestClearIgnoredVersions(t *testing.T) {
	s := testStore(t)

	if err := s.AddIgnoredVersion("app", "v1.0"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearIgnoredVersions("app"); err != nil {
		t.Fatal(err)
	}

	versions, err := s.GetIgnoredVersions("app")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 0 {
		t.Errorf("expected empty after clear, got %v", versions)
	}
}

func TestIgnoredVersionsIsolation(t *testing.T) {
	s := testStore(t)

	if err := s.AddIgnoredVersion("app-a", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddIgnoredVersion("app-b", "v2"); err != nil {
		t.Fatal(err)
	}

	va, err := s.GetIgnoredVersions("app-a")
	if err != nil {
		t.Fatal(err)
	}
	vb, err := s.GetIgnoredVersions("app-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(va) != 1 || va[0] != "v1" {
		t.Errorf("app-a versions = %v, want [v1]", va)
	}
	if len(vb) != 1 || vb[0] != "v2" {
		t.Errorf("app-b versions = %v, want [v2]", vb)
	}
}

// ---------------------------------------------------------------------------
// Registry: Rate Limits
// ---------------------------------------------------------------------------

func TestRateLimitsRoundTrip(t *testing.T) {
	s := testStore(t)

	got, err := s.LoadRateLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for empty, got %q", got)
	}

	data := []byte(`{"docker.io":{"remaining":100}}`)
	if err := s.SaveRateLimits(data); err != nil {
		t.Fatal(err)
	}

	got, err = s.LoadRateLimits()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

// ---------------------------------------------------------------------------
// Registry: GHCR Cache
// ---------------------------------------------------------------------------

func TestGHCRCacheRoundTrip(t *testing.T) {
	s := testStore(t)

	got, err := s.LoadGHCRCache()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for empty, got %q", got)
	}

	data := []byte(`{"ghcr.io/org/repo":{"alt":"docker.io/org/repo"}}`)
	if err := s.SaveGHCRCache(data); err != nil {
		t.Fatal(err)
	}

	got, err = s.LoadGHCRCache()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}
