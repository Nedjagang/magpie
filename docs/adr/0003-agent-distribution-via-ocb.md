# 3. Build the agent with OCB; don't fork the OTel Collector

Date: 2026-04-21

## Status

**Superseded by [ADR 0008](0008-upstream-otelcol-contrib.md) on 2026-04-25.** Kept for historical context; the original rationale (no fork, stay on vanilla YAML) still holds, we just arrive at that same outcome via the upstream pre-built binary instead of building our own distribution via OCB.

## Context

The Magpie Agent embeds an OpenTelemetry Collector. Three ways to package it:

1. **Fork** the upstream collector repo and maintain our own version.
2. **Use the OpenTelemetry Collector Builder (OCB)** to produce a custom distribution from upstream components.
3. **Depend on a third-party distribution** (Grafana Alloy, AWS ADOT, Splunk OTel).

Forking means permanent merge conflicts against an upstream that releases every ~2 weeks. Third-party distributions tie us to a vendor's priorities and often deviate from vanilla YAML.

OCB is the official, supported way to produce custom collector distributions. It takes a manifest of receivers/processors/exporters and compiles them into a single binary.

## Decision

Use **OCB** to build the Magpie Collector distribution. Do **not** fork. Stay on vanilla upstream YAML configuration.

The OCB manifest lives at [`agent/builder/manifest.yaml`](../../agent/builder/manifest.yaml) and is the source of truth for what Magpie supports.

## Consequences

- **No merge burden** — we consume upstream releases via module versions.
- **Upgrading** is a manifest version bump + rebuild; automatable in CI.
- **Scope is explicit**: "Magpie supports component X" equals "X is in our OCB manifest."
- **We don't invent config syntax** — Magpie YAML is collector YAML. Presets are YAML fragments, not a DSL.
- **Upstream breaking changes** can reach users — we need a version-pinning and testing strategy.
