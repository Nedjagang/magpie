// Package machineid resolves a stable per-host identifier so the agent's
// OpAMP InstanceUid can be derived deterministically rather than randomly.
//
// Without a stable seed, every agent restart produces a fresh InstanceUid
// and the control-plane registers it as a new host — operators end up with
// duplicate rows for a single machine after each service restart, reboot,
// or reinstall. With a seed, the same physical host always upserts into
// the same registry row.
//
// Seed sources are platform-specific (see machineid_windows.go and
// machineid_other.go). Callers should treat the seed as opaque and hash it
// before exposing it to the network — the raw value is considered locally
// sensitive on some platforms (e.g. Linux's /etc/machine-id is documented
// as not-for-export per machine-id(5)).
package machineid

// Seed returns a stable per-host identifier as an opaque string. Empty
// string + non-nil error means the platform's preferred source was
// unreadable; callers should fall back to a random+persisted UID and log
// the underlying error so misconfiguration is visible.
//
// The string is not guaranteed to have any particular format — only that
// repeated calls on the same host return the same value across reboots
// and reinstalls of the agent.
//
// Implementations live in machineid_windows.go and machineid_other.go.
