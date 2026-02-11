package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		s.Close()
		os.Remove(path)
	})
	return s
}

func TestResolvePolicyOverride(t *testing.T) {
	db := newTestStore(t)
	_ = db.SetPolicyOverride("myapp", "pinned")

	r := ResolvePolicy(db, map[string]string{"sentinel.policy": "auto"}, "myapp", "v1.0", "manual")
	if r.Policy != "pinned" {
		t.Fatalf("expected pinned, got %s", r.Policy)
	}
	if r.Source != SourceOverride {
		t.Fatalf("expected override source, got %s", r.Source)
	}
}

func TestResolvePolicyLabel(t *testing.T) {
	db := newTestStore(t)

	r := ResolvePolicy(db, map[string]string{"sentinel.policy": "auto"}, "myapp", "v1.0", "manual")
	if r.Policy != "auto" {
		t.Fatalf("expected auto, got %s", r.Policy)
	}
	if r.Source != SourceLabel {
		t.Fatalf("expected label source, got %s", r.Source)
	}
}

func TestResolvePolicyDefault(t *testing.T) {
	db := newTestStore(t)

	r := ResolvePolicy(db, map[string]string{}, "myapp", "v1.0", "manual")
	if r.Policy != "manual" {
		t.Fatalf("expected manual, got %s", r.Policy)
	}
	if r.Source != SourceDefault {
		t.Fatalf("expected default source, got %s", r.Source)
	}
}

func TestResolvePolicyOverrideTakesPrecedence(t *testing.T) {
	db := newTestStore(t)
	_ = db.SetPolicyOverride("myapp", "auto")

	// Label says pinned, override says auto — override wins.
	r := ResolvePolicy(db, map[string]string{"sentinel.policy": "pinned"}, "myapp", "v1.0", "manual")
	if r.Policy != "auto" {
		t.Fatalf("expected auto (override), got %s", r.Policy)
	}
	if r.Source != SourceOverride {
		t.Fatalf("expected override source, got %s", r.Source)
	}
}

func TestResolvePolicyDeleteOverride(t *testing.T) {
	db := newTestStore(t)
	_ = db.SetPolicyOverride("myapp", "pinned")
	_ = db.DeletePolicyOverride("myapp")

	r := ResolvePolicy(db, map[string]string{"sentinel.policy": "auto"}, "myapp", "v1.0", "manual")
	if r.Policy != "auto" {
		t.Fatalf("expected auto (label fallback), got %s", r.Policy)
	}
	if r.Source != SourceLabel {
		t.Fatalf("expected label source, got %s", r.Source)
	}
}

func TestResolvePolicyLatestTagAutoPolicy(t *testing.T) {
	db := newTestStore(t)

	// :latest tag with no label should resolve to auto via SourceLatest.
	r := ResolvePolicy(db, map[string]string{}, "myapp", "latest", "manual")
	if r.Policy != "auto" {
		t.Fatalf("expected auto, got %s", r.Policy)
	}
	if r.Source != SourceLatest {
		t.Fatalf("expected latest source, got %s", r.Source)
	}
}

func TestResolvePolicyEmptyTagAutoPolicy(t *testing.T) {
	db := newTestStore(t)

	// Empty tag (implicit latest) with no label should resolve to auto via SourceLatest.
	r := ResolvePolicy(db, map[string]string{}, "myapp", "", "manual")
	if r.Policy != "auto" {
		t.Fatalf("expected auto, got %s", r.Policy)
	}
	if r.Source != SourceLatest {
		t.Fatalf("expected latest source, got %s", r.Source)
	}
}

func TestResolvePolicyLatestTagLabelWins(t *testing.T) {
	db := newTestStore(t)

	// :latest tag but explicit label "pinned" — label should win over latest-auto.
	r := ResolvePolicy(db, map[string]string{"sentinel.policy": "pinned"}, "myapp", "latest", "manual")
	if r.Policy != "pinned" {
		t.Fatalf("expected pinned (label), got %s", r.Policy)
	}
	if r.Source != SourceLabel {
		t.Fatalf("expected label source, got %s", r.Source)
	}
}

func TestResolvePolicyLatestTagOverrideWins(t *testing.T) {
	db := newTestStore(t)
	_ = db.SetPolicyOverride("myapp", "pinned")

	// :latest tag but DB override "pinned" — override should win.
	r := ResolvePolicy(db, map[string]string{}, "myapp", "latest", "manual")
	if r.Policy != "pinned" {
		t.Fatalf("expected pinned (override), got %s", r.Policy)
	}
	if r.Source != SourceOverride {
		t.Fatalf("expected override source, got %s", r.Source)
	}
}

func TestResolvePolicyNonLatestUsesDefault(t *testing.T) {
	db := newTestStore(t)

	// Non-latest tag with no label should use global default.
	r := ResolvePolicy(db, map[string]string{}, "myapp", "v2.1.0", "manual")
	if r.Policy != "manual" {
		t.Fatalf("expected manual (default), got %s", r.Policy)
	}
	if r.Source != SourceDefault {
		t.Fatalf("expected default source, got %s", r.Source)
	}
}

func TestValidatePolicy(t *testing.T) {
	for _, p := range []string{"auto", "manual", "pinned"} {
		if err := ValidatePolicy(p); err != nil {
			t.Errorf("expected %q to be valid, got: %v", p, err)
		}
	}
	if err := ValidatePolicy("bogus"); err == nil {
		t.Error("expected error for bogus policy")
	}
}
