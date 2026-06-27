# Magpie Architecture

> This document describes what Magpie actually is as of **v0.1**. Items marked *(planned)* are on the roadmap but not shipped — see [LIMITATIONS.md](../LIMITATIONS.md). For the rationale behind specific choices, see [ADRs](adr/).

## System overview

```
              ┌────────────────────────────────────┐
              │  magpied  (control plane)          │
              │                                    │
              │  ┌─────────┐   ┌────────┐          │    ┌────────────────┐
              │  │  OpAMP  │   │  REST  │◀────REST─┼────┤  Next.js UI    │
              │  │  server │   │  API   │          │    │  :12001        │
              │  └────┬────┘   └────┬───┘          │    └────────────────┘
              │       │             │              │
              │  ┌────▼─────────────▼─────────┐    │
              │  │      SQLite                │    │
              │  │  configs / audit / labels  │    │
              │  └────────────────────────────┘    │
              └──────────────┬─────────────────────┘
                             │  OpAMP over WebSocket
                             │
        ┌────────────────────┼─────────────────────┐
        │                    │                     │
┌───────▼────────┐  ┌────────▼────────┐   ┌────────▼────────┐
│  magpie-agent  │  │  magpie-agent   │   │  magpie-agent   │
│  (Linux VM)    │  │  (Windows VM)   │   │  (k8s pod)      │
│       │        │  │       │         │   │       │         │
│       │ spawns │  │       │ spawns  │   │       │ spawns  │
│       ▼        │  │       ▼         │   │       ▼         │
│ otelcol-contrib│  │ otelcol-contrib │   │ otelcol-contrib │
│  (upstream)    │  │  (upstream)     │   │  (upstream)     │
└────────┬───────┘  └────────┬────────┘   └────────┬────────┘
         │                   │                     │
         └───────────────────┴─────────────────────┘
                             │ OTLP
                             ▼
              ┌────────────────────────────────────┐
              │  Any OTLP-compatible backend       │
              │  (Grafana, Datadog, Honeycomb, …)  │
              └────────────────────────────────────┘
```

## Components

### Agent (`magpie-agent`)

A single Go process per host. It is a supervisor, not a collector:

1. Connects to `magpied` over OpAMP (WebSocket).
2. Advertises its host attributes, including identifying labels `magpie.product` and `magpie.variant` (from `MAGPIE_PRODUCT` / `MAGPIE_VARIANT` env vars).
3. Receives the collector config matching its effective labels.
4. Writes the config to a local file and spawns `otelcol-contrib` (the collector) as a subprocess.
5. Reports health and applied-config status back via OpAMP.
6. Keeps its subprocess running across control-plane outages — the collector's lifetime is decoupled from the OpAMP connection.

The collector is the upstream [`otelcol-contrib`](https://github.com/open-telemetry/opentelemetry-collector-releases) binary, shipped unmodified (see [ADR 0008](adr/0008-upstream-otelcol-contrib.md)). The Magpie release pipeline downloads it, verifies its SHA256 against the upstream-published `checksums.txt`, and bundles it alongside `magpie-agent`. There is no magpie-specific collector build.

### Control plane (`magpied`)

A single Go binary exposing:

- **OpAMP server** at `/v1/opamp` for agent management (config push, health ingest, status). `opamp-go` under the hood.
- **REST API** at `/api/v1/*` for the UI and external callers.
- **SQLite persistence** at `$MAGPIE_DB_PATH` (default `magpie.db`). Schema managed with `goose`. Three tables:
  - `configs` — every config revision, keyed by `(product, variant)`.
  - `audit_log` — append-only record of every create/rollback/delete/relabel.
  - `agent_labels` — server-side label overrides keyed by OpAMP instance UID.
- No in-memory cache / queue dependency; agent connection state is reconstituted on reconnect.

### UI

Next.js 15 + React 19 dashboard at `:12001` (served separately in v0.1; embedding into the `magpied` binary is *(planned)*). Pure frontend — all state lives in `magpied`.

## Data flow

### Config publish

1. Operator authors YAML in the UI, pressing Publish.
2. UI calls `POST /api/v1/configs` with `{name, product, variant, yaml}` and an `X-Magpie-Actor` header.
3. `magpied` runs structural YAML validation (*semantic validation via the collector's own loader is planned*).
4. New row is inserted into `configs`. An audit record is written.
5. `magpied` calls `Reconcile`: for every connected agent, it resolves the effective `(product, variant)` (applying any override from `agent_labels`), looks up the latest config for that pair, and pushes it over OpAMP if the agent's currently-applied hash differs.
6. Each agent applies the config and reports `APPLIED` or `FAILED` with an error message.

End-to-end latency: ~2 seconds on a local network.

### Telemetry flow

Agents export telemetry **directly** to the OTLP backend defined in their config. The control plane is a management plane, not a data plane — no metric flows through `magpied`. This is deliberate:

- `magpied` stays small: CPU and memory usage are insensitive to your fleet's telemetry volume.
- Control-plane outages do not affect telemetry delivery.
- Horizontal scale is a fleet problem, not a Magpie problem.

### Label resolution order

When resolving the config to push to an agent:

1. If an override row exists in `agent_labels` for the agent's instance UID, use it.
2. Otherwise use `magpie.product` / `magpie.variant` advertised by the agent's OpAMP `AgentDescription`.
3. If neither matches an existing config row, fall back to `(default, default)`.
4. If that is also missing, no config is pushed — the agent stays idle.

## Non-goals (v0.1)

- **No backend / storage for telemetry.** Magpie only ships configs; the backend of your choice stores telemetry.
- **No auto-instrumentation via eBPF.** Not on the v0.1 roadmap.
- **No alerting engine.** Delegate to your backend.
- **No custom config DSL.** You edit the collector's native YAML. Templates in the UI are starting points, not a DSL.
- **No production-grade safety story yet.** See [LIMITATIONS.md](../LIMITATIONS.md). Staged rollouts, semantic validation, and HA are planned for subsequent releases.

## Rollout safety *(planned)*

The control plane's resolution function is cohort-wide today. Planned safety features:

- **Staged rollouts** — push to N % of the target cohort first, watch health for a configurable window, then promote or revert.
- **Automatic rollback** — if the canary's `Healthy` ratio drops below threshold, revert to the previous revision.
- **Semantic YAML validation** — run the collector's own config loader at publish time, not just structural checks.
- **Last-known-good guarantees** — already partially in place (agents keep running the last-applied config if magpied is unreachable); formalise with TTL and checksum verification.

These are the things that turn "fleet-management feature" into "fleet-management product." They are tracked as load-bearing requirements for v1.0, not nice-to-haves.
