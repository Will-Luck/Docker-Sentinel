package verify

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
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
	// identity and issuer are the certificate identity and OIDC issuer a
	// keyless signature must carry; identityRegexp matches them as
	// regular expressions.
	identity, issuer string
	identityRegexp   bool
	log              Logger
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

// WithKeylessIdentity sets the certificate identity (for GitHub Actions,
// the workflow URL with its ref) and the OIDC issuer a keyless signature
// must carry. With regexp they are matched as regular expressions. Keyless
// verification is refused without them: any Sigstore certificate would do.
func WithKeylessIdentity(identity, issuer string, regexp bool) Option {
	return func(v *Verifier) {
		v.identity, v.issuer, v.identityRegexp = identity, issuer, regexp
	}
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
		if v.identity == "" || v.issuer == "" {
			return &Result{Error: "keyless verification needs the expected certificate identity and OIDC issuer"}
		}
		identityFlag, issuerFlag := "--certificate-identity", "--certificate-oidc-issuer"
		if v.identityRegexp {
			identityFlag, issuerFlag = identityFlag+"-regexp", issuerFlag+"-regexp"
		}
		args = append(args, identityFlag, v.identity, issuerFlag, v.issuer)
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
