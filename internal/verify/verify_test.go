package verify

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type testLogger struct{}

func (l *testLogger) Info(msg string, args ...any)  {}
func (l *testLogger) Error(msg string, args ...any) {}
func (l *testLogger) Warn(msg string, args ...any)  {}

func TestParseMode(t *testing.T) {
	tests := []struct {
		input string
		want  Mode
	}{
		{"disabled", ModeDisabled},
		{"warn", ModeWarn},
		{"enforce", ModeEnforce},
		{"WARN", ModeWarn},
		{"Enforce", ModeEnforce},
		{"", ModeDisabled},
		{"invalid", ModeDisabled},
	}
	for _, tt := range tests {
		if got := ParseMode(tt.input); got != tt.want {
			t.Errorf("ParseMode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolveMode(t *testing.T) {
	tests := []struct {
		name         string
		label        string
		perContainer Mode
		global       Mode
		want         Mode
	}{
		{"label wins over all", "enforce", ModeWarn, ModeDisabled, ModeEnforce},
		{"label disabled explicitly", "disabled", ModeWarn, ModeEnforce, ModeDisabled},
		{"per-container wins over global", "", ModeWarn, ModeEnforce, ModeWarn},
		{"global fallback", "", "", ModeEnforce, ModeEnforce},
		{"all empty is disabled", "", "", ModeDisabled, ModeDisabled},
		{"label warn overrides enforce global", "warn", "", ModeEnforce, ModeWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveMode(tt.label, tt.perContainer, tt.global)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVerifyEmptyRef(t *testing.T) {
	v := New(&testLogger{})
	result := v.Verify(context.Background(), "")
	if result.Verified {
		t.Error("expected not verified for empty ref")
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
}

func TestVerifyNoKeyOrKeyless(t *testing.T) {
	v := New(&testLogger{})
	result := v.Verify(context.Background(), "nginx:latest")
	if result.Verified {
		t.Error("expected not verified without key or keyless")
	}
	if result.Error != "no verification key or keyless mode configured" {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

func TestAvailable(t *testing.T) {
	// Test with a non-existent binary.
	v := New(&testLogger{}, WithCosignPath("/nonexistent/cosign"))
	if v.Available() {
		t.Error("expected not available for non-existent path")
	}
}

// fakeCosign writes a script that records its arguments and succeeds.
func fakeCosign(t *testing.T) (path, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	path = filepath.Join(dir, "cosign")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // the fake cosign must be executable
		t.Fatal(err)
	}
	return path, argsFile
}

// Keyless verification without an expected identity and issuer accepts a
// signature from anyone with a Sigstore certificate, so it is refused.
func TestKeylessNeedsIdentityAndIssuer(t *testing.T) {
	path, argsFile := fakeCosign(t)
	v := New(nil, WithCosignPath(path), WithKeyless())

	res := v.Verify(context.Background(), "app@sha256:abc")

	if res.Verified || !strings.Contains(res.Error, "identity") {
		t.Errorf("result = %+v, want keyless refused without an identity", res)
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Error("cosign ran with no identity to check")
	}
}

// With an identity and issuer, cosign is asked to check exactly those.
func TestKeylessChecksTheIdentity(t *testing.T) {
	for _, tc := range []struct {
		regexp bool
		want   []string
	}{
		{false, []string{"--certificate-identity", "https://github.com/o/r/.github/workflows/release.yml@refs/heads/main", "--certificate-oidc-issuer", "https://token.actions.githubusercontent.com"}},
		{true, []string{"--certificate-identity-regexp", "https://github.com/o/r/.github/workflows/release.yml@refs/heads/main", "--certificate-oidc-issuer-regexp", "https://token.actions.githubusercontent.com"}},
	} {
		path, argsFile := fakeCosign(t)
		v := New(nil, WithCosignPath(path), WithKeyless(),
			WithKeylessIdentity("https://github.com/o/r/.github/workflows/release.yml@refs/heads/main", "https://token.actions.githubusercontent.com", tc.regexp))

		if res := v.Verify(context.Background(), "app@sha256:abc"); !res.Verified {
			t.Fatalf("result = %+v", res)
		}
		raw, _ := os.ReadFile(argsFile)
		got := strings.Fields(string(raw))
		if !slices.Equal(got, append([]string{"verify", "app@sha256:abc"}, tc.want...)) {
			t.Errorf("cosign args = %v, want the identity checks %v", got, tc.want)
		}
	}
}

// The refusal says exactly which two settings to set.
func TestKeylessRefusalNamesTheSettings(t *testing.T) {
	path, _ := fakeCosign(t)
	res := New(nil, WithCosignPath(path), WithKeyless()).Verify(context.Background(), "app@sha256:abc")
	for _, want := range []string{"cosign_identity", "cosign_oidc_issuer", "SENTINEL_COSIGN_IDENTITY", "SENTINEL_COSIGN_OIDC_ISSUER"} {
		if !strings.Contains(res.Error, want) {
			t.Errorf("error %q does not name %s", res.Error, want)
		}
	}
}

// The identity is read at each verification, so a change saved in the
// dashboard applies without a restart.
func TestKeylessIdentityIsReadAtEachCheck(t *testing.T) {
	path, argsFile := fakeCosign(t)
	current := KeylessIdentity{}
	v := New(nil, WithCosignPath(path), WithKeyless(), WithKeylessIdentitySource(func() KeylessIdentity { return current }))

	if res := v.Verify(context.Background(), "app@sha256:abc"); res.Verified {
		t.Fatal("verified without an identity")
	}
	current = KeylessIdentity{Identity: "me@example.com", Issuer: "https://accounts.example.com"}
	if res := v.Verify(context.Background(), "app@sha256:abc"); !res.Verified {
		t.Fatalf("result = %+v, want the saved identity used", res)
	}
	raw, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(raw), "me@example.com") {
		t.Errorf("cosign args = %q, want the new identity", raw)
	}
}

type settingsMap map[string]string

func (m settingsMap) load(key string) (string, error) { return m[key], nil }

// A saved setting wins; one never saved falls back to its environment
// variable.
func TestKeylessIdentityFromSettingsAndEnv(t *testing.T) {
	t.Setenv("SENTINEL_COSIGN_IDENTITY", "env-id")
	t.Setenv("SENTINEL_COSIGN_OIDC_ISSUER", "env-issuer")
	t.Setenv("SENTINEL_COSIGN_IDENTITY_REGEXP", "true")

	got := KeylessIdentityFrom(settingsMap{}.load)()
	if got != (KeylessIdentity{Identity: "env-id", Issuer: "env-issuer", Regexp: true}) {
		t.Errorf("from env = %+v", got)
	}
	got = KeylessIdentityFrom(settingsMap{"cosign_identity": "saved-id", "cosign_identity_regexp": "false"}.load)()
	if got != (KeylessIdentity{Identity: "saved-id", Issuer: "env-issuer", Regexp: false}) {
		t.Errorf("saved over env = %+v", got)
	}
}
