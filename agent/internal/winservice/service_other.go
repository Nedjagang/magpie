//go:build !windows

package winservice

import (
	"errors"
	"log/slog"
)

var errUnsupported = errors.New("Windows Service management is only supported on Windows; on Linux use systemd (see docs/onboarding.md)")

// IsServiceMode is always false on non-Windows.
func IsServiceMode() bool { return false }

// Run is a no-op on non-Windows; the caller should run in foreground instead.
func Run(_ *slog.Logger, _ RunFunc) error { return errUnsupported }

// Install is a stub on non-Windows.
func Install(_ *slog.Logger) error { return errUnsupported }

// Uninstall is a stub on non-Windows.
func Uninstall(_ *slog.Logger) error { return errUnsupported }

// Status is a stub on non-Windows.
func Status(_ *slog.Logger) error { return errUnsupported }

// SetMachineEnv is a stub on non-Windows.
func SetMachineEnv(_, _ string) error { return errUnsupported }

// WriteParams is a stub on non-Windows.
func WriteParams(_ map[string]string) error { return errUnsupported }

// ReadParams is a stub on non-Windows — returns nil, nil so the agent's
// cross-platform startup code doesn't error.
func ReadParams() (map[string]string, error) { return nil, nil }

// DeleteParams is a stub on non-Windows.
func DeleteParams() error { return nil }
