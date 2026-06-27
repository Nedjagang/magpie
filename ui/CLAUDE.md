# Magpie UI

Next.js 15 dashboard for config authoring, fleet management, and agent onboarding.

## Architecture

- Next.js 15 App Router with React 19
- Tailwind CSS 4 (beta) for styling via PostCSS
- TypeScript in strict mode
- Communicates with magpied REST API (default http://localhost:12002)

## Key Files & Directories

- `app/page.tsx` — main dashboard page
- `app/layout.tsx` — root layout with metadata and global styles
- `app/logo.tsx` — branding component
- `app/globals.css` — Tailwind imports and global styles
- `lib/api.ts` — REST client for magpied control plane API
- `lib/templates.ts` — starter YAML templates (Windows, Linux, Kubernetes, custom)
- `lib/format.ts` — UI formatting helpers
- `tsconfig.json` — TypeScript config (strict, path aliases via `@/*`)
- `next.config.mjs` — Next.js configuration
- `postcss.config.mjs` — PostCSS with Tailwind plugin

## Development

```bash
pnpm install --frozen-lockfile  # install dependencies
pnpm dev                        # dev server on http://localhost:12001
pnpm run build                  # production build (also type-checks)
pnpm run lint                   # Next.js linting
```

## Conventions

- Uses pnpm as package manager (lockfile: `pnpm-lock.yaml`)
- Dev server runs on port 12001 (not default 3000)
- Path alias `@/*` maps to the `ui/` root
- No test framework configured yet (v0.1)

## Common Pitfalls

- React 19 is a release candidate — some type definitions may lag behind
- Tailwind CSS 4 is beta — check compatibility when adding plugins
- The UI expects magpied running on localhost:12002 — check `lib/api.ts` for the base URL