package verify

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// Mode determines verification behaviour.
type Mode string

const (
	ModeDisabled Mode = "disabled" // skip verification (default)
	ModeWarn     Mode = "warn"     // log warning on failure, proceed
	ModeEnforce  Mode = "enforce"  // block update on failure
)

// ParseMode converts a string to a Mode. Returns ModeDisabled for unknown values.
func ParseMode(s string) Mode {
	switch strings.ToLower(s) {
	case "warn":
		return ModeWarn
	case "enforce":
		return ModeEnforce
	default:
		return ModeDisabled
	}
}

// Result holds the outcome of a signature verification.
type Result struct {
	Verified bool   `json:"verified"`
	Error    string `json:"error,omitempty"`
	Output   string `json:"output,omitempty"` // cosign stdout for debugging
}

// Logger is a minimal logging interface.
type Logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
	Warn(msg string, args ...any)
}

// Verifier checks image signatures using the cosign CLI.
type Verifier struct {
	cosignPath string // path to cosign binary (default: "cosign")
	keyPath    string // path to public key PEM for keyed verification
	keyless    bool   // use Sigstore keyless (Fulcio + Rekor)
	// identity returns the certificate identity and OIDC issuer a keyless
	// signature must carry, read at each verification.
	identity func() KeylessIdentity
	log      Logger
}

// KeylessIdentity is what a keyless signature's certificate must show: the
// identity (for GitHub Actions, the workflow URL with its ref) and the OIDC
// issuer, matched exactly or, with Regexp, as regular expressions.
type KeylessIdentity struct {
	Identity, Issuer string
	Regexp           bool
}

// MissingKeylessIdentity says what to set when keyless verification has no
// identity to check.
const MissingKeylessIdentity = "keyless verification needs the expected certificate identity and OIDC issuer: " +
	"set cosign_identity and cosign_oidc_issuer in the settings " +
	"(or SENTINEL_COSIGN_IDENTITY and SENTINEL_COSIGN_OIDC_ISSUER)"

// Complete reports whether both the identity and the issuer are set.
func (k KeylessIdentity) Complete() bool { return k.Identity != "" && k.Issuer != "" }

// KeylessIdentityFrom returns a source that reads the keyless identity from
// the saved settings (load) at each call. A setting never saved, or saved
// empty, falls back to its environment variable: SENTINEL_COSIGN_IDENTITY,
// SENTINEL_COSIGN_OIDC_ISSUER and SENTINEL_COSIGN_IDENTITY_REGEXP.
func KeylessIdentityFrom(load func(key string) (string, error)) func() KeylessIdentity {
	get := func(key, env string) string {
		if load != nil {
			if v, err := load(key); err == nil && v != "" {
				return v
			}
		}
		return os.Getenv(env)
	}
	return func() KeylessIdentity {
		return KeylessIdentity{
			Identity: get(store.SettingCosignIdentity, "SENTINEL_COSIGN_IDENTITY"),
			Issuer:   get(store.SettingCosignOIDCIssuer, "SENTINEL_COSIGN_OIDC_ISSUER"),
			Regexp:   get(store.SettingCosignIdentityRegexp, "SENTINEL_COSIGN_IDENTITY_REGEXP") == "true",
		}
	}
}

// Option configures a Verifier.
type Option func(*Verifier)

// WithCosignPath sets the path to the cosign binary.
func WithCosignPath(path string) Option {
	return func(v *Verifier) { v.cosignPath = path }
}

// WithKeyPath sets the public key for keyed verification.
func WithKeyPath(path string) Option {
	return func(v *Verifier) { v.keyPath = path }
}

// WithKeyless enables Sigstore keyless verification.
func WithKeyless() Option {
	return func(v *Verifier) { v.keyless = true }
}

// WithKeylessIdentity sets a fixed certificate identity and OIDC issuer a
// keyless signature must carry (see KeylessIdentity). Keyless verification
// is refused without them: any Sigstore certificate would do.
func WithKeylessIdentity(identity, issuer string, regexp bool) Option {
	id := KeylessIdentity{Identity: identity, Issuer: issuer, Regexp: regexp}
	return WithKeylessIdentitySource(func() KeylessIdentity { return id })
}

// WithKeylessIdentitySource reads the keyless identity from src at each
// verification, so a change applies without a restart.
func WithKeylessIdentitySource(src func() KeylessIdentity) Option {
	return func(v *Verifier) { v.identity = src }
}

// New creates a Verifier with the given options.
func New(log Logger, opts ...Option) *Verifier {
	v := &Verifier{
		cosignPath: "cosign",
		log:        log,
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

// Problem returns why every check would fail as configured, or "": today
// only keyless verification without an identity (MissingKeylessIdentity).
func (v *Verifier) Problem() string {
	if v.keyPath != "" || !v.keyless {
		return ""
	}
	if v.identity == nil || !v.identity().Complete() {
		return MissingKeylessIdentity
	}
	return ""
}

// Available checks whether the cosign binary is installed and reachable.
func (v *Verifier) Available() bool {
	_, err := exec.LookPath(v.cosignPath)
	return err == nil
}

// Verify checks the signature of the given image reference.
// Returns a Result indicating whether the signature is valid.
func (v *Verifier) Verify(ctx context.Context, imageRef string) *Result {
	if imageRef == "" {
		return &Result{Error: "empty image reference"}
	}

	args := []string{"verify", imageRef}

	if v.keyPath != "" {
		args = append(args, "--key", v.keyPath)
	} else if v.keyless {
		// Keyless verification uses Sigstore's transparency log and proves
		// only who signed: without the expected identity and issuer any
		// Sigstore certificate would pass, so it is refused.
		var id KeylessIdentity
		if v.identity != nil {
			id = v.identity()
		}
		if !id.Complete() {
			return &Result{Error: MissingKeylessIdentity}
		}
		identityFlag, issuerFlag := "--certificate-identity", "--certificate-oidc-issuer"
		if id.Regexp {
			identityFlag, issuerFlag = identityFlag+"-regexp", issuerFlag+"-regexp"
		}
		args = append(args, identityFlag, id.Identity, issuerFlag, id.Issuer)
	} else {
		return &Result{Error: "no verification key or keyless mode configured"}
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, v.cosignPath, args...) //nolint:gosec // cosignPath is operator-configured, not user input
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// cosign exits non-zero when verification fails.
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return &Result{
			Verified: false,
			Error:    errMsg,
			Output:   strings.TrimSpace(stdout.String()),
		}
	}

	return &Result{
		Verified: true,
		Output:   strings.TrimSpace(stdout.String()),
	}
}
