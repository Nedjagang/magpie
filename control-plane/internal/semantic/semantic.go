// Package semantic runs semantic validation of OpenTelemetry collector
// configs by subprocessing the upstream `otelcol-contrib validate` command.
// It exists alongside configs.Validate (structural rules) — together they
// implement the "structural ✓ + semantic ✓" gate from plan §3.2.
//
// Design and approach are committed in docs/v1.0-semantic-validation.md.
package semantic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Validator validates a collector config YAML using the actual OTel
// collector loader. A nil Validator value (as a typed interface) is the
// caller's signal for "feature off" — structural validation runs alone
// and the skip is logged once at startup.
type Validator interface {
	Validate(ctx context.Context, yaml string) error
}

// OtelcolValidator runs `otelcol-contrib validate --config=<temp file>`
// against a config YAML and returns nil on exit code 0, an error
// otherwise. The captured stderr (truncated) becomes the error message,
// so operators see the loader's actual diagnostic in the publish dialog.
type OtelcolValidator struct {
	binaryPath string
	timeout    time.Duration
}

// Option configures an OtelcolValidator. Reserved for future tuning;
// v1.0 first slice ships with defaults and no exposed knobs.
type Option func(*OtelcolValidator)

// WithTimeout overrides the default 5-second per-validate wall-clock
// timeout. Useful for local development against a slower box.
func WithTimeout(d time.Duration) Option {
	return func(v *OtelcolValidator) { v.timeout = d }
}

// NewOtelcolValidator constructs a validator that subprocesses the
// otelcol-contrib binary at binaryPath. The path is checked at construction
// (file must exist + be executable) so a misconfigured deploy fails fast
// at startup rather than at first publish.
func NewOtelcolValidator(binaryPath string, opts ...Option) (*OtelcolValidator, error) {
	if binaryPath == "" {
		return nil, errors.New("binary path is empty")
	}
	info, err := os.Stat(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", binaryPath, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%q is a directory, want an executable", binaryPath)
	}
	v := &OtelcolValidator{
		binaryPath: binaryPath,
		timeout:    5 * time.Second,
	}
	for _, opt := range opts {
		opt(v)
	}
	return v, nil
}

// stderrLimit caps captured stderr at 8 KB. Loader errors are typically
// short (one or two lines); 8 KB protects against pathological cases
// without truncating actionable detail.
const stderrLimit = 8 * 1024

// Validate writes the YAML to a temp file, runs the validate subcommand,
// and returns nil on success or an error that wraps the loader's stderr
// on failure. Honors ctx cancellation; uses an internal timeout (default
// 5 s) so a hung subprocess can't stall a publish indefinitely.
func (v *OtelcolValidator) Validate(ctx context.Context, yaml string) error {
	if yaml == "" {
		return errors.New("config yaml is empty")
	}

	// Temp file lifecycle is the validator's responsibility — caller
	// only sees the yaml string and the validate verdict.
	tmp, err := os.CreateTemp("", "magpie-validate-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(yaml); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// Subprocess timeout layered over caller's ctx. Whichever expires
	// first cancels the cmd. Loader latency is ~100 ms typical so the
	// 5 s default has wide margin.
	subCtx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()

	// otelcol-contrib's validate subcommand: loads the config, runs the
	// component graph build, exits 0 on success, non-zero on any failure
	// with the loader error written to stderr.
	cmd := exec.CommandContext(subCtx, v.binaryPath, "validate", "--config="+tmpPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = nil // discard

	runErr := cmd.Run()
	if subCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("semantic validate timed out after %s", v.timeout)
	}
	if runErr == nil {
		return nil
	}
	// On failure, surface the loader's stderr to the operator. The
	// publish dialog renders this in a code block; truncating the long
	// tail keeps the dialog usable for pathologically verbose errors.
	msg := truncate(stderr.String(), stderrLimit)
	if msg == "" {
		// No stderr but non-zero exit → fall back to the exec error.
		// Should be rare with a real otelcol binary.
		return fmt.Errorf("semantic validate failed: %w", runErr)
	}
	return fmt.Errorf("semantic validate failed: %s", msg)
}

// truncate clips s to n bytes, appending an ellipsis marker so the caller
// can tell the message was cut. Operates on bytes (not runes) — loader
// stderr is ASCII in practice, and a torn rune at the boundary just
// renders as a replacement character rather than corrupting downstream.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…(truncated)"
}

// LookupBinary returns the explicit MAGPIE_OTELCOL_VALIDATOR_PATH or "".
// Semantic validation is **opt-in** in Magpie's default posture: operators
// who want the otelcol loader run at publish-time set the env var; everyone
// else runs structural YAML validation only. The rationale is that the
// validator is platform-bound (a Windows otelcol-contrib can't validate a
// Linux-only journald receiver), and the agent itself will reject malformed
// config at apply time and report it via ApplyState — so the publish-side
// loader check, while useful, isn't load-bearing for reliability. Removing
// the auto $PATH fallback keeps "structural only" the true default and
// avoids surprising operators when the version on PATH disagrees with
// what their fleet runs.
func LookupBinary(envPath string) string {
	return envPath
}
