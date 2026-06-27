# Magpie

OpenTelemetry fleet management control plane. Manages distributed OTel collectors via the OpAMP protocol without custom DSLs or Ansible.

@agent/CLAUDE.md
@control-plane/CLAUDE.md
@ui/CLAUDE.md
@docs/v1.0-plan.md

## Repository Structure

- `agent/` — Go agent supervisor wrapping upstream otelcol-contrib
- `control-plane/` — Go control plane (magpied): OpAMP server, REST API, SQLite
- `ui/` — Next.js 15 dashboard for config authoring and fleet management
- `packaging/` — Dockerfile, docker-compose, Helm chart
- `docs/` — Architecture docs, onboarding guide, ADRs

## Prerequisites

- Go 1.25+
- Node 20+ with pnpm 9+
- golangci-lint (for `make lint`)

## Setup

```bash
make tidy                          # tidy Go modules
cd ui && pnpm install --frozen-lockfile  # install UI deps
```

## Build

```bash
make build       # builds bin/magpied + bin/magpie-agent
```

## Test

```bash
make test        # Go tests with race detector (both modules)
make lint        # golangci-lint (both modules)
cd ui && pnpm run lint   # Next.js lint
cd ui && pnpm run build  # UI type-check + build
```

## Local Development

```bash
make quickstart  # builds, then launches magpied on :12002 and UI on :12001
```

## Docker

```bash
make docker      # build images
make docker-up   # start compose stack (UI :12001, magpied :12002)
make docker-down # stop
```

## Key Environment Variables

- `MAGPIE_SERVER_URL` — control plane URL for agents
- `MAGPIE_PRODUCT` / `MAGPIE_VARIANT` — config cohort identity
- `MAGPIE_AGENT_NAME` — human-friendly agent name
- `MAGPIE_HTTP_ADDR` — magpied listen address (default :12002)
- `MAGPIE_DB_PATH` — SQLite database path
- `MAGPIE_RELEASES_DIR` — directory for agent binary distribution

## Conventions

- Two separate Go modules: `agent/go.mod` and `control-plane/go.mod`
- Upstream otelcol-contrib is shipped unmodified (see ADR 0008)
- OTELCOL_VERSION must stay in sync between Makefile and `.github/workflows/release.yml`
- All config changes are tracked in an append-only audit log
- Actor identity comes from `X-Magpie-Actor` HTTP header