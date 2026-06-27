# Architecture Decision Records

We use the [MADR](https://adr.github.io/madr/) format for decisions.

| # | Title | Status |
| --- | --- | --- |
| [0001](0001-name-magpie.md) | Name: Magpie | Accepted |
| [0002](0002-license-apache-2.0.md) | License: Apache 2.0 + DCO | Accepted |
| [0003](0003-agent-distribution-via-ocb.md) | Build agent with OCB, don't fork | Superseded by 0008 |
| [0004](0004-opamp-for-control.md) | Use OpAMP for agent management | Accepted |
| [0005](0005-go-for-control-plane.md) | Control plane in Go | Accepted |
| [0006](0006-monorepo-layout.md) | Monorepo layout | Accepted |
| [0007](0007-sqlite-default-postgres-optional.md) | SQLite default, Postgres optional | Accepted |
| [0008](0008-upstream-otelcol-contrib.md) | Ship upstream otelcol-contrib, not OCB distro | Accepted |

## Adding an ADR

1. Copy the next available number.
2. Use the MADR structure: **Context → Decision → Consequences**.
3. Start the ADR in `Proposed` status; promote to `Accepted` after review.
4. When superseded, mark as `Superseded by ADR-XXXX` — do not delete.
