# Magpie Agent

OpAMP-aware supervisor that wraps the upstream OpenTelemetry Collector (otelcol-contrib). Connects to the magpied control plane, receives config updates, and manages the collector subprocess lifecycle.

## Architecture

- Single Go binary (`magpie-agent`) that exec's `otelcol-contrib` as a child process
- Communicates with magpied via OpAMP WebSocket (`/v1/opamp`)
- Reports health status, applied config hash, and effective config back to control plane
- Caches last-known-good config locally so the collector survives control-plane outages
- Derives a stable `InstanceUid` from the machine ID (not random per restart)

## Key Files & Directories

- `cmd/magpie-agent/main.go` — entry point, bootstrap config from env vars, OpAMP client setup
- `internal/collector/supervisor.go` — collector process start/restart/stop lifecycle
- `internal/collector/supervisor_test.go` — regression tests for outage resilience
- `internal/machineid/` — stable machine identifier (platform-specific: Linux, Windows, macOS)
- `internal/winservice/` — Windows Service integration (SCM lifecycle hooks)

## Development

```bash
make build-agent                    # build bin/magpie-agent
cd agent && go test -race ./...     # run agent tests
cd agent && golangci-lint run       # lint
```

## Key Dependencies

- `github.com/open-telemetry/opamp-go` — OpAMP protocol client
- `golang.org/x/sys` — system calls for machine ID and Windows service

## Conventions

- Pure Go, no cgo (`CGO_ENABLED=0` for cross-compilation)
- Platform-specific code uses `_windows.go` / `_other.go` build tags
- The agent never modifies the collector binary — it exec's whatever is at `BinaryPath`
- Bootstrap config comes entirely from environment variables, not config files

## Common Pitfalls

- The collector subprocess must keep running even when the control plane is unreachable — test this invariant
- `InstanceUid` is derived from machine ID, not randomly generated — changing derivation breaks agent identity
- OTELCOL_VERSION in Makefile and release.yml must stay in sync