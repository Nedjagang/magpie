# 5. Control plane written in Go

Date: 2026-04-21

## Status

Accepted

## Context

The control plane needs a language and runtime. Candidates:

- **Go** — matches the OTel ecosystem; reference OpAMP server and supervisor are in Go.
- **TypeScript / Node.js** — good UI-stack alignment; would require wrapping `opamp-go` via HTTP/gRPC or reimplementing OpAMP.
- **Rust** — performant, but OpAMP ecosystem is less mature.

## Decision

Write the control plane in **Go**.

Initial stack:

- HTTP router: **`github.com/go-chi/chi/v5`** (stdlib-shaped, minimal)
- DB driver: **`github.com/jackc/pgx/v5`**
- SQL: **`sqlc`** (generated, type-safe queries)
- Migrations: **`github.com/pressly/goose/v3`**
- Structured logging: **`log/slog`** (stdlib, Go 1.21+)
- OpAMP: **`github.com/open-telemetry/opamp-go`**
- Self-telemetry: OpenTelemetry Go SDK

## Consequences

- **Shared language** with the agent supervisor and `opamp-go` — no FFI or cross-language wrappers.
- **Single binary** distribution; easy to containerize and cross-compile.
- **Hiring / contribution pool** is smaller than Node for frontend-heavy engineers, but aligned with the infra-engineer profile of contributors we expect.
- **UI is still Next.js/TypeScript** and talks to the Go API over REST — two languages in the monorepo is accepted cost.
- **We avoid ORMs** (GORM, ent) in favor of `sqlc` for predictability and migration clarity.
