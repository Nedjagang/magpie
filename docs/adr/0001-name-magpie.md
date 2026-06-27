# 1. Name: Magpie

Date: 2026-04-21

## Status

Accepted

## Context

The project needs a name that is short, memorable, and free of heavy trademark / naming collisions. It should hint at observability without being a literal compound of "telemetry" or "observability."

Several candidates were evaluated:

- **lumi / lumen** — crowded (Pulumi's old name, Laravel Lumen, hardware brands, skincare trademarks)
- **otelith** — unique but awkward to say
- **prism** — negative NSA connotation; also used by Stoplight Prism
- **glint** — trademark-adjacent to LinkedIn Glint (Microsoft)
- **magpie** — less crowded; strong metaphor (magpies collect shiny things — like a collector gathers telemetry signals)

## Decision

Adopt **Magpie** as the project name.

Rationale:

1. Strong, concrete metaphor: a magpie is a collector — exactly the role of an OpenTelemetry Collector.
2. Friendly and memorable; supports a mascot and visual identity.
3. Not blocked by a dominant trademark or GitHub repo in the observability space.
4. Easy to say, spell, and fit into domains.

## Consequences

- Need to verify and register domains and social handles before any public release.
- If a blocking trademark surfaces during a formal trademark search, we can rename — most code references flow through a single `magpie` string constant or module path, so global renames are cheap at this stage.
- Go module path is `github.com/magpie-project/magpie/...` as a placeholder until a final org is chosen.
