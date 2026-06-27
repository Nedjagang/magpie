# 6. Monorepo layout

Date: 2026-04-21

## Status

Accepted

## Context

Magpie has multiple first-party components: agent, control plane, UI, packaging. These change together — e.g., a new preset requires agent manifest updates + UI changes + docs + possibly DB migrations.

Options:

- **Polyrepo** — separate repos per component. Independent versioning; cross-component changes require coordinated PRs.
- **Monorepo** — single repo with top-level directories per component.

## Decision

Use a **monorepo** with the following top-level layout:

```
magpie/
├── agent/            # Supervisor (runs upstream otelcol-contrib)
├── control-plane/    # Go: magpied binary
├── ui/               # Next.js dashboard
├── packaging/        # nfpm, WiX, Helm, Docker
├── docs/             # Architecture, ADRs, user docs
└── .github/          # CI, issue templates
```

Releases use a **single project version** (Magpie v0.x.y). Sub-components ship together under that version.

## Consequences

- **Atomic cross-component changes** in a single PR.
- **Single CI pipeline** with path filters to skip unchanged components.
- **Single release version** keeps the user story simple ("I'm running Magpie v1.2.0").
- **Repo size** grows over time — we can adopt sparse checkouts or partial clones in CI if needed.
- **Release orchestration** requires careful tagging and signing (handled by `goreleaser` from `.github/workflows/release.yml` when it lands).
- **IDE friction** for polyglot contributors — mitigated by the `go.work` and workspace config to come.
