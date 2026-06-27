//go:build !windows

package machineid

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Seed reads systemd's /etc/machine-id, falling back to D-Bus's
// /var/lib/dbus/machine-id on systems that have the latter but not the
// former (older non-systemd Linux). Both are 32 hex chars and stable for
// the OS install lifetime — see machine-id(5).
//
// Returns an error if neither file is readable or both are empty. On
// macOS this will fail (no /etc/machine-id by default); the agent will
// fall back to a random+persisted UID, which the user will see logged.
// macOS support via IOPlatformUUID can be added later if needed.
func Seed() (string, error) {
	candidates := []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}
	var firstErr error
	for _, p := range candidates {
		body, err := os.ReadFile(p)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		id := strings.TrimSpace(string(body))
		if id != "" {
			return id, nil
		}
	}
	if firstErr == nil {
		firstErr = errors.New("no machine-id source readable")
	}
	return "", fmt.Errorf("read machine-id: %w", firstErr)
}
