# Magpie Control Plane

Single Go binary (`magpied`) that exposes:

- OpAMP server for agent management
- REST API for the UI and external integrations
- Embedded static UI (at release time)

## Layout

- `cmd/magpied/` — entrypoint
- `internal/api/` — REST handlers (chi)
- `internal/opamp/` — OpAMP server adapter
- `internal/db/` — sqlc-generated queries, goose migrations
- `internal/config/` — server configuration loader

## Development

```bash
# From repo root
make build          # builds ./bin/magpied
./bin/magpied       # starts the server (stub at this stage)
```

## Planned dependencies

Added incrementally as each feature lands, to keep the dependency graph minimal at each step:

- `github.com/go-chi/chi/v5` — HTTP routing
- `github.com/jackc/pgx/v5` — Postgres driver
- `github.com/open-telemetry/opamp-go` — OpAMP server
- `github.com/pressly/goose/v3` — DB migrations
- `go.opentelemetry.io/otel` — self-telemetry
- `github.com/sqlc-dev/sqlc` (dev-only, not a runtime dep) — SQL codegen

## Configuration

Server config is loaded from (in priority order):

1. CLI flags
2. Environment variables (prefixed `MAGPIE_`)
3. Config file (`--config path/to/magpied.yaml`)
4. Defaults

Secrets (DB password, signing keys) are **not** read from config files. They are read from env or a secrets file referenced by path.
