# Magpie Agent

The Magpie Agent is an OpAMP-aware supervisor that wraps the upstream OpenTelemetry Collector. It does **not** build its own collector distribution — it runs `otelcol-contrib` published by the OpenTelemetry project, unmodified (see [ADR 0008](../docs/adr/0008-upstream-otelcol-contrib.md)).

## Layout

- `cmd/magpie-agent/` — the supervisor entry point.
- `internal/collector/` — `otelcol-contrib` process lifecycle (start, restart on config change, graceful stop).
- `internal/winservice/` — Windows Service integration.

## Getting the collector binary

The supervisor exec's whatever sits at `BinaryPath` (by default, `otelcol-contrib[.exe]` next to `magpie-agent`). For local development:

```bash
# From the repo root — downloads + SHA256-verifies upstream otelcol-contrib
make fetch-collector
```

For production, use the release artifacts — the magpie release zip already contains `otelcol-contrib` alongside `magpie-agent`.

If you want a different OTel distribution (e.g. the smaller `otelcol` core binary, or a vendor fork), point `MAGPIE_COLLECTOR_BINARY` at it. The supervisor doesn't care what's at that path as long as it accepts `--config=<path.yaml>`.

## Runtime behavior

1. Reads bootstrap configuration from Machine-scope env vars (Windows) or unit-file env (Linux). See [docs/onboarding.md](../docs/onboarding.md) for what each variable controls.
2. Opens an OpAMP connection to the control plane.
3. Loads the last-known-good config from disk (if any) and starts the collector.
4. On `RemoteConfig` messages, writes the new config to disk, restarts the collector, and reports `APPLIED` / `FAILED` + live `ComponentHealth` via OpAMP.
5. If the collector binary isn't at `BinaryPath`, reports `FAILED` + `Healthy: false` with a clear error message — never silently degrades.

## Development status

Pre-alpha. Tracking upstream otelcol-contrib releases manually; auto-bump via Renovate/Dependabot is on the roadmap.
