//go:build windows

package machineid

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Seed reads HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid, which Windows
// generates at install time and preserves across reboots, agent reinstalls,
// and user account changes. It only changes if the OS itself is reinstalled
// or the value is manually edited — exactly the semantic we want for
// "is this the same physical host as last time".
//
// Read with WOW64_64KEY so 32-bit builds don't get redirected to the
// Wow6432Node mirror, which on some systems holds a different (or missing)
// value.
func Seed() (string, error) {
	k, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Cryptography`,
		registry.QUERY_VALUE|registry.WOW64_64KEY,
	)
	if err != nil {
		return "", fmt.Errorf("open Cryptography key: %w", err)
	}
	defer k.Close()

	guid, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return "", fmt.Errorf("read MachineGuid: %w", err)
	}
	guid = strings.TrimSpace(guid)
	if guid == "" {
		return "", fmt.Errorf("MachineGuid is empty")
	}
	return guid, nil
}
