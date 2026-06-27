# 4. Use OpAMP for agent management

Date: 2026-04-21

## Status

Accepted

## Context

The control plane needs to:

- Push configuration to agents.
- Receive health, version, and applied-config status from agents.
- Coordinate agent package updates.
- Identify and authenticate agents.

Options:

- **Custom protocol** over HTTP / WebSocket / gRPC.
- **OpAMP** (Open Agent Management Protocol), a spec under the OpenTelemetry project.

OpAMP is an open specification designed specifically for managing OTel (and other) agents. It has a reference Go server and client at [`github.com/open-telemetry/opamp-go`](https://github.com/open-telemetry/opamp-go), and there is a reference supervisor in `opentelemetry-collector-contrib`. Real deployments exist at observIQ, Honeycomb, and Dash0.

## Decision

Use **OpAMP** as the sole protocol between the Magpie control plane and Magpie agents, via the reference Go implementation.

## Consequences

- **We don't design a protocol** — 3–6 months of work avoided.
- **Interoperability**: Magpie agents can, in principle, be managed by any OpAMP server; the Magpie control plane can manage any OpAMP-compatible agent. Aligns with the vendor-neutral positioning.
- **We adopt the OpAMP data model**: `AgentDescription`, `RemoteConfig`, `RemoteConfigStatus`, `PackageStatus`, etc. These shape our DB schema and API.
- **OpAMP does not orchestrate progressive rollouts** — we must implement group-based rollouts, canaries, and rollback in the control-plane layer.
- **The spec is still evolving** — we will track spec versions and upstream fixes where needed.
