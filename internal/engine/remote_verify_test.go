package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
)

// An update dispatched to an agent passes the same signature gate as a
// local one, on the hub, before the agent is asked to do anything.
func TestVerifyRemoteImage(t *testing.T) {
	unsigned := &verify.Result{Verified: false, Error: "no matching signatures"}
	signed := &verify.Result{Verified: true}
	enforceLabel := map[string]string{verify.ContainerLabel: "enforce"}
	cases := []struct {
		name      string
		mode      verify.Mode
		labels    map[string]string
		verifier  *mockImageVerifier // nil: cosign not available
		wantErr   bool
		wantCheck bool
	}{
		{name: "enforce, unsigned", mode: verify.ModeEnforce, verifier: &mockImageVerifier{result: unsigned}, wantErr: true, wantCheck: true},
		{name: "enforce, signed", mode: verify.ModeEnforce, verifier: &mockImageVerifier{result: signed}, wantCheck: true},
		{name: "warn, unsigned", mode: verify.ModeWarn, verifier: &mockImageVerifier{result: unsigned}, wantCheck: true},
		{name: "disabled", mode: verify.ModeDisabled, verifier: &mockImageVerifier{result: unsigned}},
		{name: "label enforces", mode: verify.ModeDisabled, labels: enforceLabel, verifier: &mockImageVerifier{result: unsigned}, wantErr: true, wantCheck: true},
		{name: "enforce, no cosign", mode: verify.ModeEnforce, wantErr: true},
		{name: "warn, no cosign", mode: verify.ModeWarn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, u := setupUpdateWithScanVerify(t)
			u.SetVerifyMode(tc.mode)
			if tc.verifier != nil {
				u.SetVerifier(tc.verifier)
			}
			err := u.VerifyRemoteImage(context.Background(), "web", tc.labels, "nginx:2")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if tc.verifier == nil && tc.wantErr && !errors.Is(err, ErrVerifierUnavailable) {
				t.Errorf("err = %v, want ErrVerifierUnavailable", err)
			}
			if tc.verifier != nil {
				checked := len(tc.verifier.calls) == 1 && tc.verifier.calls[0] == "nginx:2"
				if checked != tc.wantCheck {
					t.Errorf("verifier calls %v, want a check of nginx:2: %v", tc.verifier.calls, tc.wantCheck)
				}
			}
		})
	}
}
