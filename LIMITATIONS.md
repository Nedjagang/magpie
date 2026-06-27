# Limitations — v0.2

This document lists what Magpie does **not** do yet, so you can decide whether it fits your use case. Every item is a known gap, on the roadmap, and not a surprise.

If you're looking at Magpie for production use, the short version is: **gated**. v0.2 added shared-token auth, so a single team running it inside a known network is fine. Multi-tenant or externally exposed deployments still need the items flagged 🔴 to land first.

---

## 🟢 What v0.2 added

| | |
|---|---|
| **Shared bearer-token auth** | Set `MAGPIE_API_TOKEN` on `magpied` to require `Authorization: Bearer …` on every REST and OpAMP request. Empty value keeps v0.1 no-auth behavior with a startup warning, so upgrades roll forward gracefully. |
| **Audit actor bound to auth state** | Audit rows are prefixed `authenticated:` when the caller passed the bearer check, `anonymous:` otherwise. `X-Magpie-Actor` is now a label, not an identity claim. |
| **Append-only audit log enforced at DB** | SQLite triggers `RAISE(ABORT)` on `UPDATE`/`DELETE` of `audit_log`. |
| **CORS allowlist** | `MAGPIE_ALLOWED_ORIGINS` (comma-separated). Default empty → no cross-origin requests. Wildcard `*` is gone. |
| **Body-size cap on writes** | 1 MiB on POST/PUT bodies; 413 on overrun. |
| **`secure_delete(ON)`** | Deleted SQLite rows zero their pages, so revoked configs don't linger as readable bytes. |
| **Tighter agent config perms** | `0o600` on the on-disk config file; existing v0.1 installs auto-converge on agent restart. |
| **Loopback-only UI port** in compose | UI on `127.0.0.1:12001`; `:12002` still external for agent reachability with a comment recommending a reverse proxy. |
| **`MAGPIE_PUBLIC_URL`** | Operator-controlled public URL for install scripts; `r.Host` is no longer the source of truth. |
| **otelcol component allowlist** | `MAGPIE_ALLOWED_RECEIVERS`, `_PROCESSORS`, `_EXPORTERS`, `_EXTENSIONS`, `_CONNECTORS` (CSV). Configs referencing component types outside the allowlist are rejected at publish time with a 400. Empty list = no enforcement on that section. |
| **Optional TLS** | Set `MAGPIE_TLS_CERT` and `MAGPIE_TLS_KEY` to PEM file paths. magpied calls `ListenAndServeTLS` and pins `tls.VersionTLS12` as the floor. Setting only one of the two refuses-to-start instead of silently falling back to plain HTTP. Reverse-proxy TLS termination remains the recommended deployment for v0.2; this is for setups that want magpied to terminate directly. |
| **Cosign signing of the agent binary** | CI signs `magpie-agent` keyless via Sigstore Fulcio (workflow OIDC identity) on every tag push. Each release zip carries the detached `.sig` and `.cert` next to the binary, magpied serves them at `/api/v1/releases/<os>/<arch>/{signature,certificate}`, and both install scripts verify before extracting when `cosign` is on PATH. Set `MAGPIE_REQUIRE_SIGNATURE=1` to fail-closed when cosign is missing or the release isn't signed. Default is warn-and-continue so v0.1 fleets keep installing while signing rolls out. |

## 🔴 Security — blocking for hostile networks

| | |
|---|---|
| **Single shared API token (v0.2 MVP)** | One token authorizes every operator and every agent. Leak from any host = full control plane compromise. v0.3 will add per-agent enrollment tokens and OIDC for operators. For now: rotate by setting a new `MAGPIE_API_TOKEN` and re-running install. |
| **No authorization / RBAC** | Every authed caller has god mode — no read-only tokens, no per-cohort scoping. |
| **Secrets in plaintext** | OTLP tokens embedded in config YAML are stored in SQLite and retained in revision history forever. Rotating a compromised token requires manual cleanup. |
| **TLS is opt-in** | `magpied` listens on plain HTTP/WS unless `MAGPIE_TLS_CERT` + `MAGPIE_TLS_KEY` are set. Reverse-proxy termination is the simpler path; the env-var path exists for setups where adding a proxy layer is more friction than dropping cert files. No automatic cert reload — restart on rotation. |
| **otelcol component allowlist is opt-in** | Set `MAGPIE_ALLOWED_RECEIVERS` / `_PROCESSORS` / `_EXPORTERS` to constrain which otelcol components an operator can deploy. Empty (the default) means any otelcol component passes — including `filelog: /etc/shadow`. The opt-in default keeps v0.1 → v0.2 upgrades smooth; tighten it as soon as you know which components your fleet legitimately uses. |

**Planned:** per-agent enrollment tokens (v0.3), OIDC integration (v0.3), secret references (v0.3), default-deny component allowlist (v0.3).

---

## 🟠 Safety — blocking for large fleets

| | |
|---|---|
| **No staged rollouts** | Publish broadcasts to 100 % of matching agents immediately. No canary, no "deploy to 5 % first." One bad publish can reach every host in a cohort in 2 seconds. |
| **YAML validation is structural, not semantic** | Magpie parses YAML, checks pipelines reference defined receivers/exporters, and rejects obvious mistakes. It does **not** run the collector's own config loader. A typo in a processor field will pass validation and crash the collector on apply. |
| **Per-variant fallback only** | Agents whose `(product, variant)` has no config fall back to `default/default`. No finer-grained targeting (no labels beyond those two, no selectors). |

**Planned:** semantic validation via the OTel config loader (v0.2), canary rollouts with promotion gates (v0.3).

---

## 🟡 Scale / availability

| | |
|---|---|
| **SQLite-only** | Persistence is a single SQLite file. Safe up to a few hundred agents; beyond that, concurrent write contention will appear. |
| **Single-process `magpied`** | No clustering, no leader election. If `magpied` dies, config pushes and UI are unavailable until it's back. Telemetry keeps flowing — agents continue running the last-applied config — but you can't change anything. |
| **No meta-observability** | `magpied` emits no self-metrics. You cannot monitor the tool that monitors your fleet. Planned: Prometheus endpoint + per-agent export stats reported over OpAMP. |

**Planned:** Postgres option (v0.2), multi-magpied with shared Postgres (v0.4), `/metrics` endpoint (v0.2).

---

## 🟡 Operations

| | |
|---|---|
| **No agent self-update** | Upgrading the agent is your job — you ship a new binary to hosts yourself. |
| **No GitOps reconcile** | Configs are authored in the UI or via REST; there is no "sync from a git repo" mode. |
| **No CLI** | Everything is REST. A `magpiectl` binary for CI pipelines is planned. |
| **Pagination limited** | The Audit endpoint caps at 500 entries; Configs and Agents endpoints return full sets. Fine for dev fleets, not for long-lived installations. |

---

## 🟢 UX rough edges (cosmetic, not blocking)

- YAML editor is a plain textarea — no syntax highlighting, no linting, no autocomplete. Monaco or CodeMirror is on the roadmap.
- No diff view between the current active config and a proposed new one.
- Fleet table (on the All-hosts view) lacks sorting and filtering by status/health beyond a text search.
- Placeholder logo / favicon. A proper magpie mark is yet to land.

---

## Tested against

| | Version |
|---|---|
| Go | 1.22, 1.25 |
| Node | 20, 22 |
| pnpm | 9 |
| OTel Collector Builder | v0.116.0 |
| SQLite | via modernc.org/sqlite 1.49 |
| Browser | Chrome 125+, Firefox 125+, Safari 17+ |
| OS | Windows 11, Ubuntu 24.04 |

---

## Who should run Magpie today

- Teams running OpenTelemetry collectors on 10–200 VMs who want faster iteration than Ansible.
- Teams evaluating collector-management strategies before choosing Alloy, Operator, or roll-your-own.
- Contributors and open-source tinkerers.

## Who should NOT run Magpie today

- Anything externally reachable or multi-tenant.
- Compliance-heavy environments (SOC 2, HIPAA, PCI) where audit trails must be tamper-resistant.
- Fleets above ~500 hosts until the Postgres + HA story lands.
- Anywhere you can't tolerate a 1-click-breaks-everything cohort broadcast.

If none of the above excludes you, clone and try it. Issues, PRs, and hate-mail all welcome.
