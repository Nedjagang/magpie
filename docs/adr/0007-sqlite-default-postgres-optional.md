# 7. SQLite as default persistence, Postgres optional

Date: 2026-04-22

## Status

Accepted. Supersedes the Postgres-only decision implied in [0005](0005-go-for-control-plane.md) and [architecture.md](../architecture.md).

## Context

Magpie ships as a one-command install. Requiring an external PostgreSQL server contradicts that: the operator must install Postgres, create a database, manage credentials, and run migrations before `magpied` starts. For small and medium fleets — the common case — this friction is unjustified.

The control plane's workload is low-write (config changes, agent heartbeats batched, audit log). A single-file embedded store handles it comfortably.

## Decision

The default persistence backend is **SQLite** via `modernc.org/sqlite` (pure-Go, no CGO). The database file lives next to the binary (`./magpie.db`) or wherever `MAGPIE_DB_PATH` points. Migrations are embedded via `embed.FS` and applied automatically on startup using `pressly/goose` as a library.

Postgres remains a supported backend for HA / multi-node control planes, selected via `MAGPIE_DATABASE_URL`. The persistence layer is abstracted behind an interface so both drivers coexist.

## Consequences

- Zero-config first run: `./magpied` is enough.
- No external service to install, back up, or monitor for single-node deployments.
- SQL written must be portable across SQLite and Postgres, or dialect-gated.
- HA control planes must opt into Postgres explicitly.
