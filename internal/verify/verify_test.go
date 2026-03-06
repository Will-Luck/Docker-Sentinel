package verify

import (
	"context"
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
