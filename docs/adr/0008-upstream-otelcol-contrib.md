# 8. Ship upstream otelcol-contrib instead of building a custom distribution

Date: 2026-04-25

## Status

Accepted. Supersedes [ADR 0003](0003-agent-distribution-via-ocb.md).

## Context

ADR 0003 chose the OpenTelemetry Collector Builder (OCB) to produce a Magpie-specific distribution (`magpie-otelcol`). After v0.1 alpha we have real signal on what that cost:

- **Version bumps are manual.** ~25 component versions in `agent/builder/manifest.yaml` have to be moved in lockstep, OCB re-run, the regenerated `agent/collector/` directory re-committed, CI reverified. Each release cycle eats a day.
- **The "custom distribution" surface we control is not being used.** We've added no magpie-specific receivers, processors, or exporters. The OCB manifest is a curated subset of upstream components — not anything bespoke.
- **Security-patch latency is worse than upstream.** OTel ships patches to otelcol-contrib the same day; we ship them only after a human does the bump dance above.
- **The OCB-generated module adds build complexity** (second `go.mod`, second module in the workspace, custom Makefile rules) that a small team can't maintain well.

The reasons for OCB over forking (ADR 0003) remain valid: we want vanilla OTel YAML, we don't want merge burden. But OCB's benefits over the pre-built upstream binary are theoretical — attack-surface narrowing, binary-size reduction, custom components — and none of them are paying off in practice.

## Decision

**Ship the upstream `otelcol-contrib` binary unmodified.**

- CI downloads the pre-built binary from [opentelemetry-collector-releases](https://github.com/open-telemetry/opentelemetry-collector-releases/releases) for each platform.
- We verify against the release's published `checksums.txt` before packaging.
- The binary is bundled in the Magpie release zip **with its upstream name** (`otelcol-contrib` / `otelcol-contrib.exe`) and **its upstream checksum** intact. No renaming, no rebuild, no modification.
- Magpie releases pin a specific `OTELCOL_VERSION` (currently `v0.116.0`). Bumping that one value is the entire upgrade procedure — no OCB regen, no manifest editing.

The agent supervisor (`agent/internal/collector/supervisor.go`) already execs whatever binary is at `BinaryPath`; all that changes is the default name.

## Consequences

**Positive**
- **Zero bespoke build infrastructure.** One `curl` + `sha256sum` in CI replaces the entire OCB pipeline.
- **Same-day security patches.** Upstream ships, we bump `OTELCOL_VERSION`, we ship.
- **Upstream-attested supply chain.** Operators can verify their `otelcol-contrib` binary's SHA against the public upstream release — the single clearest story for enterprise review.
- **No divergence risk.** There is no Magpie-ism that can drift from upstream; the binary *is* upstream.
- **Smaller codebase.** Deletes `agent/builder/` and `agent/collector/`; removes a second Go module.

**Negative (accepted)**
- **Bigger binary.** `otelcol-contrib` is ~170 MB vs. our curated OCB build's ~85 MB. Onboarding over slow WAN links feels this. Mitigation: the release workflow compresses to zip before serving; operators can also use the OTel `otelcol` core binary if they explicitly choose that tradeoff.
- **Broader attack surface.** Components we don't use (opampextension, k8sattributesprocessor, etc.) are still in the binary. Mitigation: they're dormant unless a config references them, and we control the config channel via OpAMP.
- **Config-compat risk.** When upstream renames a field, users pinned to the old Magpie release keep working; users who upgrade see the break at upgrade time instead of at their-chosen-moment. Mitigation: version pin per Magpie release (not float), surface upstream release notes in our changelog, and long-term decouple agent/collector upgrades via OpAMP `PackagesAvailable` (see roadmap).

## Reversibility

The `Supervisor` type exec's whatever binary sits at `BinaryPath`. Switching back to an OCB-built distribution is ~50 lines of workflow + Makefile change plus re-introducing `agent/collector/` from git history. No runtime interface break. This decision is **not a one-way door.**

## Related

- [ADR 0003](0003-agent-distribution-via-ocb.md) — original OCB decision (superseded).
- Roadmap: OpAMP `AgentCapabilities_AcceptsPackages` for fleet-wide collector version management.
