package main

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Will-Luck/Docker-Sentinel/internal/config"
)

// errPasskeysDisabled wraps every reason the passkey configuration cannot be
// used. Passkeys are then off; password and SSO sign-in are unaffected.
var errPasskeysDisabled = errors.New("passkeys disabled")

// passkeyServer is the part of the web server that passkey set-up needs.
type passkeyServer interface {
	SetWebAuthn(wa *webauthn.WebAuthn)
}

// newPasskeys returns the WebAuthn instance for the configured relying party,
// or an error wrapping errPasskeysDisabled. WebAuthn needs the RPID to be a
// domain: an IP address or a single label other than localhost is refused.
func newPasskeys(cfg *config.Config) (*webauthn.WebAuthn, error) {
	if err := protocol.ValidateRPID(cfg.WebAuthnRPID); err != nil {
		return nil, fmt.Errorf("%w: SENTINEL_WEBAUTHN_RPID must be a hostname with a dot, or localhost (%w)",
			errPasskeysDisabled, err)
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName: cfg.WebAuthnDisplayName,
		RPID:          cfg.WebAuthnRPID,
		RPOrigins:     cfg.WebAuthnOriginList(),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errPasskeysDisabled, err)
	}
	return wa, nil
}

// setupPasskeys turns passkeys on in srv when an RPID is configured. When the
// configuration cannot be used it logs why and leaves passkeys off, so a bad
// RPID never stops Sentinel from starting.
func setupPasskeys(cfg *config.Config, srv passkeyServer, log *slog.Logger) {
	if !cfg.WebAuthnEnabled() {
		return
	}
	wa, err := newPasskeys(cfg)
	if err != nil {
		log.Error(err.Error(), "rpid", cfg.WebAuthnRPID, "origins", cfg.WebAuthnOrigins)
		return
	}
	srv.SetWebAuthn(wa)
	log.Info("webauthn passkeys enabled", "rpid", cfg.WebAuthnRPID, "origins", cfg.WebAuthnOrigins)
}
