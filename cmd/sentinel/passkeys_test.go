package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Will-Luck/Docker-Sentinel/internal/config"
)

// recordingPasskeyServer stands in for the web server and records what
// passkey set-up hands it.
type recordingPasskeyServer struct {
	calls int
	wa    *webauthn.WebAuthn
}

func (r *recordingPasskeyServer) SetWebAuthn(wa *webauthn.WebAuthn) {
	r.calls++
	r.wa = wa
}

func passkeyConfig(rpid, origin string) *config.Config {
	return &config.Config{
		WebAuthnRPID:        rpid,
		WebAuthnDisplayName: "Docker-Sentinel",
		WebAuthnOrigins:     origin,
	}
}

func TestNewPasskeys(t *testing.T) {
	tests := []struct {
		name     string
		rpid     string
		origin   string
		disabled bool
	}{
		{"IP address", "192.0.2.10", "https://192.0.2.10:8443", true},
		{"single label", "sentinel", "https://sentinel:8443", true},
		{"localhost", "localhost", "http://localhost:8080", false},
		{"dotted hostname", "sentinel.example.com", "https://sentinel.example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wa, err := newPasskeys(passkeyConfig(tt.rpid, tt.origin))
			if tt.disabled {
				if !errors.Is(err, errPasskeysDisabled) {
					t.Fatalf("newPasskeys(%q) error = %v, want errPasskeysDisabled", tt.rpid, err)
				}
				if wa != nil {
					t.Errorf("newPasskeys(%q) returned an instance with the error", tt.rpid)
				}
				return
			}
			if err != nil {
				t.Fatalf("newPasskeys(%q): %v", tt.rpid, err)
			}
			if wa == nil || wa.Config.RPID != tt.rpid {
				t.Fatalf("newPasskeys(%q) = %+v, want an instance for that RPID", tt.rpid, wa)
			}
			if _, _, err := wa.BeginDiscoverableLogin(); err != nil {
				t.Errorf("instance for %q cannot begin a login: %v", tt.rpid, err)
			}
		})
	}
}

// An RPID the WebAuthn library refuses turns passkeys off with a logged
// reason; set-up returns so start-up carries on, and the web server never
// gets a WebAuthn instance.
func TestSetupPasskeysBadRPIDDisablesPasskeys(t *testing.T) {
	for _, c := range []struct{ rpid, origin string }{
		{"192.0.2.10", "https://192.0.2.10:8443"},
		{"sentinel", "https://sentinel:8443"},
	} {
		t.Run(c.rpid, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&buf, nil))
			srv := &recordingPasskeyServer{}

			setupPasskeys(passkeyConfig(c.rpid, c.origin), srv, log)

			if srv.calls != 0 {
				t.Errorf("SetWebAuthn called %d times, want none", srv.calls)
			}
			var rec map[string]any
			if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
				t.Fatalf("want one JSON log record, got %q: %v", buf.String(), err)
			}
			if rec["level"] != "ERROR" {
				t.Errorf("level = %v, want ERROR", rec["level"])
			}
			if msg, _ := rec["msg"].(string); !strings.HasPrefix(msg, "passkeys disabled: ") {
				t.Errorf("msg = %q, want it to start with %q", msg, "passkeys disabled: ")
			}
			if rec["rpid"] != c.rpid {
				t.Errorf("rpid = %v, want %q", rec["rpid"], c.rpid)
			}
		})
	}
}

func TestSetupPasskeysEnablesPasskeys(t *testing.T) {
	var buf bytes.Buffer
	srv := &recordingPasskeyServer{}

	setupPasskeys(passkeyConfig("sentinel.example.com", "https://sentinel.example.com"), srv,
		slog.New(slog.NewJSONHandler(&buf, nil)))

	if srv.calls != 1 || srv.wa == nil {
		t.Fatalf("SetWebAuthn calls = %d instance = %v, want one call with an instance", srv.calls, srv.wa)
	}
	if strings.Contains(buf.String(), "passkeys disabled") {
		t.Errorf("unexpected disabled log: %s", buf.String())
	}
}

func TestSetupPasskeysWithoutRPIDDoesNothing(t *testing.T) {
	var buf bytes.Buffer
	srv := &recordingPasskeyServer{}

	setupPasskeys(passkeyConfig("", ""), srv, slog.New(slog.NewJSONHandler(&buf, nil)))

	if srv.calls != 0 || buf.Len() != 0 {
		t.Errorf("SetWebAuthn calls = %d log = %q, want neither", srv.calls, buf.String())
	}
}
