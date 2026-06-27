# 2. License: Apache 2.0, DCO on commits

Date: 2026-04-21

## Status

Accepted

## Context

The project should be open source, compatible with the OpenTelemetry ecosystem, and leave the door open for future commercialization paths (support, hosted SaaS, open-core add-ons).

License options considered:

- **MIT** — permissive, short; **no explicit patent grant**.
- **Apache 2.0** — permissive, explicit patent grant, OTel-standard.
- **AGPL-3.0** — copyleft; forces SaaS modifications to be open-sourced.
- **BSL (Business Source License)** — delayed open source; requires copyright ownership to adopt later.

Contributor rights model:

- **DCO (Developer Certificate of Origin)** — lightweight; signed commits, no agreement.
- **CLA (Contributor License Agreement)** — heavier; assigns or licenses contributor rights broadly.

## Decision

License the project under the **Apache License 2.0**.

Require a **DCO sign-off** on every commit (enforced by CI) instead of a CLA.

## Consequences

- **Patent grant** protects users and contributors from patent claims by contributors.
- **Ecosystem fit**: matches OpenTelemetry, Kubernetes, CNCF — zero friction for enterprise adoption.
- **Commercial flexibility retained** for non-relicensing paths: support contracts, hosted SaaS, open-core features.
- **Relicensing is harder** without a CLA: once external contributors land, we cannot unilaterally move to BSL / AGPL. If relicensing becomes plausible later, we can introduce a CLA at that point.
- **DCO is low-friction** for contributors — `git commit -s` is the full ceremony.
