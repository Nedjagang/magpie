package semantic_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/magpie-project/magpie/control-plane/internal/semantic"
)

// fakeBinary writes a tiny shell script that exits with the given code
// and writes msg to stderr. Validates the OtelcolValidator's subprocess
// + stderr-capture machinery without needing a real otelcol-contrib
// binary on the test box.
//
// Skips on Windows — the script-based approach is Unix-shell-specific
// and Linux CI provides the coverage. The OtelcolValidator itself is
// OS-agnostic; only the test fixture is.
func fakeBinary(t *testing.T, exitCode int, stderrMsg string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("subprocess fake-binary fixture is Unix-only; covered on Linux CI")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-otelcol")
	script := "#!/bin/sh\n" +
		"echo " + shellQuote(stderrMsg) + " 1>&2\n" +
		"exit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return scriptPath
}

func shellQuote(s string) string {
	// Single-quote escape so the message survives the shell's parsing
	// (newlines, special chars, etc. are passed through verbatim except
	// for an embedded single-quote, which we close-escape-reopen).
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func itoa(n int) string {
	// Avoids strconv import for the trivial codes the tests exercise.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestValidatePassesOnExitZero(t *testing.T) {
	bin := fakeBinary(t, 0, "")
	v, err := semantic.NewOtelcolValidator(bin)
	if err != nil {
		t.Fatalf("NewOtelcolValidator: %v", err)
	}
	if err := v.Validate(context.Background(), "receivers: {}\nservice: {}\n"); err != nil {
		t.Errorf("Validate on exit-0 binary returned error: %v", err)
	}
}

func TestValidateSurfacesStderrOnFailure(t *testing.T) {
	loaderMsg := "error decoding 'receivers/otlp': missing required field 'protocols'"
	bin := fakeBinary(t, 1, loaderMsg)
	v, _ := semantic.NewOtelcolValidator(bin)

	err := v.Validate(context.Background(), "anything")
	if err == nil {
		t.Fatal("Validate on exit-1 binary returned nil error")
	}
	if !strings.Contains(err.Error(), loaderMsg) {
		t.Errorf("Validate error %q does not contain loader stderr %q", err.Error(), loaderMsg)
	}
}

func TestValidateRejectsEmptyYAML(t *testing.T) {
	bin := fakeBinary(t, 0, "")
	v, _ := semantic.NewOtelcolValidator(bin)
	if err := v.Validate(context.Background(), ""); err == nil {
		t.Error("Validate on empty yaml returned nil; want error")
	}
}

func TestValidateRespectsTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based timeout test uses /bin/sh; skipping on Windows")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "slow-otelcol")
	script := "#!/bin/sh\nsleep 2\nexit 0\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write slow binary: %v", err)
	}
	v, _ := semantic.NewOtelcolValidator(scriptPath, semantic.WithTimeout(200*time.Millisecond))
	start := time.Now()
	err := v.Validate(context.Background(), "x: 1")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Validate on sleep binary returned nil; want timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error %q does not mention timeout", err.Error())
	}
	if elapsed > time.Second {
		t.Errorf("validate took %s, expected ~200ms timeout to fire promptly", elapsed)
	}
}

func TestNewOtelcolValidatorRejectsMissingBinary(t *testing.T) {
	_, err := semantic.NewOtelcolValidator(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("NewOtelcolValidator returned nil error for nonexistent binary")
	}
}

func TestNewOtelcolValidatorRejectsDirectory(t *testing.T) {
	_, err := semantic.NewOtelcolValidator(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("NewOtelcolValidator on a directory: err = %v, want one mentioning 'directory'", err)
	}
}

func TestNewOtelcolValidatorRejectsEmptyPath(t *testing.T) {
	_, err := semantic.NewOtelcolValidator("")
	if err == nil {
		t.Fatal("NewOtelcolValidator(\"\") returned nil error")
	}
}

func TestLookupBinaryHonorsExplicitEnv(t *testing.T) {
	want := "/explicit/path/otelcol-contrib"
	got := semantic.LookupBinary(want)
	if got != want {
		t.Errorf("LookupBinary(explicit path) = %q, want %q", got, want)
	}
}

func TestLookupBinaryReturnsEmptyOnEmptyEnv(t *testing.T) {
	// Semantic validation is opt-in: empty env var means "off". No $PATH
	// fallback — operators must explicitly set MAGPIE_OTELCOL_VALIDATOR_PATH.
	if got := semantic.LookupBinary(""); got != "" {
		t.Errorf("LookupBinary(\"\") returned %q, want \"\"", got)
	}
}

// stubError is here just to anchor `errors` to a real use so a future
// edit to this file doesn't drop it accidentally; the package's actual
// error wrapping happens via fmt.Errorf("%w", ...).
var _ = errors.New
