# Magpie Control Plane (magpied)

Single Go binary providing an OpAMP server, REST API, and SQLite persistence for OpenTelemetry fleet management.

## Architecture

- HTTP server (chi router) on port 12002 by default
- OpAMP WebSocket endpoint at `/v1/opamp` for agent communication
- SQLite database with goose migrations for schema management
- Agents are tracked by OpAMP InstanceUid with health and config status
- Config is versioned per (product, variant) pair with full revision history

## Key Files & Directories

- `cmd/magpied/main.go` — entry point, server setup, route registration
- `internal/opamp/server.go` — OpAMP WebSocket handler, agent registry, config push
- `internal/opamp/registry.go` — connected agent tracking by InstanceUid
- `internal/api/` — REST handlers (chi-based): config CRUD, audit log, agent list, labels, releases
- `internal/configs/store.go` — config CRUD and revision history
- `internal/configs/validate.go` — structural YAML validation (receivers/exporters/pipelines)
- `internal/audit/store.go` — append-only audit log (create/rollback/delete/relabel)
- `internal/labels/store.go` — per-agent product/variant label overrides
- `internal/agents/store.go` — fleet state tracking (connected agents, health, config status)
- `internal/releases/releases.go` — catalogs per-platform agent+collector zips from `$MAGPIE_RELEASES_DIR`
- `internal/db/db.go` — connection pool, migrations runner
- `internal/db/migrations/` — goose SQL migration files (00001–00005)
- `internal/install/install.go` — schema bootstrap and integrity checks

## Development

```bash
make build-control-plane            # build bin/magpied
cd control-plane && go test -race ./...  # run tests
cd control-plane && golangci-lint run    # lint
```

## Key Dependencies

- `github.com/go-chi/chi/v5` — HTTP routing
- `github.com/open-telemetry/opamp-go` — OpAMP server implementation
- `github.com/pressly/goose/v3` — SQL migration management
- `modernc.org/sqlite` — pure-Go SQLite (no cgo)
- `gopkg.in/yaml.v3` — YAML parsing for config validation

## Conventions

- All state lives in SQLite — no external dependencies at runtime
- Config validation is structural only (YAML well-formedness, receiver/exporter references) — semantic validation planned for v0.2
- Actor identity for audit log entries comes from `X-Magpie-Actor` HTTP header
- Migrations use goose with sequential numbering (00001, 00002, ...)
- Each internal package owns its own SQLite table(s) and provides a `store` type

## Common Pitfalls

- SQLite is single-writer — concurrent write-heavy workloads will bottleneck (~500 agents practical max)
- No authentication in v0.1 — do not expose magpied directly to untrusted networks
- Config validation does NOT run the collector's semantic checker — structurally valid YAML may still fail at the collector level
- Adding a new migration requires the next sequential number in `internal/db/migrations/`