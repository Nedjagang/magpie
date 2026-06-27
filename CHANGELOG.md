# Changelog

All notable changes to this project will be documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Magpie uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [0.2.0] — 2026-05-09

Auth + supply-chain hardening + security review fixes. Backwards-compatible:
unset `MAGPIE_API_TOKEN` keeps v0.1 no-auth behaviour with a startup warning,
so v0.1 → v0.2 upgrades roll forward without coordinated cutover.

### Added

**Bearer-token auth** — `MAGPIE_API_TOKEN` gates `/api/v1/*` and `/v1/opamp`.
Constant-time SHA256 compare, 401 with `WWW-Authenticate`, `/healthz` public.
Audit `actor` prefixed `authenticated:` / `anonymous:`. Agent attaches the
token to the OpAMP WebSocket upgrade. UI sign-in modal + localStorage. Install
scripts bake the token into systemd `EnvironmentFile` / Windows registry.

**otelcol component allowlist** — `MAGPIE_ALLOWED_RECEIVERS` /
`_PROCESSORS` / `_EXPORTERS` / `_EXTENSIONS` / `_CONNECTORS` reject configs
referencing component types outside the allowlist. Empty = no enforcement
(legacy compat). Closes the residual "filelog: /etc/shadow" risk that auth
alone leaves open.

**Optional TLS** — `MAGPIE_TLS_CERT` / `MAGPIE_TLS_KEY` enable
`ListenAndServeTLS`; TLS 1.2 floor pinned. Half-config refuses to start.

**Cosign signing** — agent binary signed keyless via Sigstore Fulcio in CI
on every tag push. Each release zip carries `.sig` + `.cert` next to the
binary; magpied serves them at `/api/v1/releases/<os>/<arch>/{signature,certificate}`.
Bash and PowerShell installers run `cosign verify-blob` when sig+cert+cosign
are all present. `MAGPIE_REQUIRE_SIGNATURE=1` fails closed.

**CORS allowlist** — `MAGPIE_ALLOWED_ORIGINS` (CSV) replaces wildcard `*`.
Cross-origin write methods from non-allowlisted origins → 403 before the
handler runs.

**`MAGPIE_PUBLIC_URL`** — install-script URL derivation now prefers the env
var over the request `Host` header (closes Host-header reflection).

**Audit-log append-only at engine level** — SQLite triggers `RAISE(ABORT)`
on `UPDATE` / `DELETE` of `audit_log`.

**Security headers** — `X-Content-Type-Options: nosniff`,
`X-Frame-Options: DENY`, `Referrer-Policy: no-referrer` on every response.

**Helm chart auth** — `magpied.auth.token` (literal, auto-generates a Secret)
or `magpied.auth.existingSecret` (BYO).

### Changed

- `pnpm-lock.yaml` regenerated; `next` 15.0.3 → 15.5.9 (CVE-2025-29927,
  CVE-2025-66478, CVE-2025-55183, CVE-2025-55184, CVE-2025-67779);
  `react` / `react-dom` RC → 19.0.0 stable.
- POST/PUT request bodies capped at 1 MiB; 413 on overrun.
- Agent on-disk config tightened to `0o600` (was `0o644`); existing files
  re-chmod'd on agent restart so the fix self-installs.
- SQLite DSN sets `secure_delete(ON)` so revoked configs (which carry OTLP
  tokens per the threat model) don't linger in unallocated pages.
- `docker-compose.yml` binds UI to `127.0.0.1:12001`; `magpied:12002` stays
  external since agents need it. Both ports take `MAGPIE_API_TOKEN` + 
  `MAGPIE_ALLOWED_ORIGINS` from the host environment.
- Install scripts emit a one-liner that references `$MAGPIE_API_TOKEN` rather
  than the literal token, so secrets stay out of operator shell history.

### Security

Closes critical and high findings from the v0.1 security review:

| ID | Class | Resolution |
|---|---|---|
| C-1, C-2 | Unauthenticated control plane → fleet-wide RCE | Bearer auth on all routes |
| C-3 | Wildcard CORS | Origin allowlist |
| C-5 | CVE-2025-29927 in Next.js 15.0.3 | Bumped to 15.5.9 |
| H-1 | Audit log not actually append-only | DB-level triggers |
| H-2 | No body-size cap | 1 MiB MaxBytesReader |
| H-3 | Agent config 0o644 | Now 0o600 |
| H-4 | Unsigned agent zip | Cosign keyless sign + verify |
| H-5 | Host-header install-script reflection | `MAGPIE_PUBLIC_URL` |
| M-2 | SQLite leaks deleted secrets | `secure_delete(ON)` |
| M-4 | Compose binds 0.0.0.0 by default | UI → loopback |
| L-1 | No security headers | nosniff / DENY / no-referrer |

See [LIMITATIONS.md](LIMITATIONS.md) for what's still on the v0.3 list.

---

## [0.1.0] — 2026-04-22

First tagged release. Internal alpha. Not production-ready — see
[LIMITATIONS.md](LIMITATIONS.md).

### Added

**Control plane (`magpied`)**
- OpAMP WebSocket server on `/v1/opamp`.
- REST API on `/api/v1/*` for listing agents, creating and rolling back configs, deleting products and variants, reading the audit log, and managing per-agent label overrides.
- SQLite persistence with `goose` migrations; schema covers `configs`, `audit_log`, `agent_labels`.
- Structural YAML validation on config publish (receivers/exporters referenced by pipelines must exist).
- Append-only audit log with actor tracking via `X-Magpie-Actor` header.
- Per-agent server-side label overrides; resolution: override first, advertised second, `default/default` fallback.
- Reconcile-on-change: every create / rollback / relabel pushes only to agents whose effective labels match.

**Agent (`magpie-agent`)**
- OpAMP client that advertises `magpie.product` and `magpie.variant` identifying attributes from `MAGPIE_PRODUCT` / `MAGPIE_VARIANT` env vars.
- Spawns and supervises upstream `otelcol-contrib` as a subprocess with the pushed config.
- Local config cache on disk so collectors survive agent restarts without control-plane reachability.

**UI**
- Next.js 15 + React 19 dashboard on `:12001`.
- Master-detail layout: product list sidebar, per-product detail pane with variant rows, scoped host table, scoped audit.
- Editor drawer with Edit and History tabs; rollback inline from any prior revision.
- Agent detail drawer with health, last config-apply error, all attributes, and label override controls.
- Onboard-host modal generating ready-to-paste PowerShell / bash commands.
- Variant templates for Windows (hostmetrics + Windows Event Log), Linux (hostmetrics + journald), Kubernetes (k8s_cluster + kubeletstats), and a minimal custom starter.

### Fixed
- Agent no longer terminates its collector when the OpAMP connection to `magpied` drops. The collector survives control-plane outages and keeps exporting telemetry using the last-applied config. Regression test at `agent/internal/collector/supervisor_test.go`.

### Known gaps
See [LIMITATIONS.md](LIMITATIONS.md).
