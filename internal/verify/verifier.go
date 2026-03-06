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
	log        Logger
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
		// Keyless verification uses Sigstore's transparency log.
		// COSIGN_EXPERIMENTAL=1 is deprecated in newer cosign versions,
		// but --certificate-identity-regexp and --certificate-oidc-issuer-regexp
		// are the new way. For broad verification, use ".*" patterns.
		args = append(args,
			"--certificate-identity-regexp", ".*",
			"--certificate-oidc-issuer-regexp", ".*",
		)
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
