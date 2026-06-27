# Magpie UI

Next.js (App Router) dashboard for the Magpie control plane.

## Planned stack

- **Next.js** (App Router) with static export
- **Tailwind CSS** + **shadcn/ui**
- **Tanstack Query** for server state
- **Zustand** for light client state (if needed)
- **Playwright** for end-to-end tests
- API client generated from the control plane's OpenAPI spec

## Status

Placeholder. Scaffolding lands in a future phase once the control plane REST API is stable enough to generate a client.

## Why separate from the control plane?

The UI has its own toolchain (Node, Next, Tailwind). Keeping it out of the Go build simplifies the control-plane image until integration time. At release, the static build output is embedded into the `magpied` binary via `embed.FS` and served from `/` by the same HTTP server that serves the API.
