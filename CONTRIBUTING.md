# Contributing to Magpie

Thanks for your interest in contributing!

## Ground rules

- Be respectful. We follow the [Contributor Covenant](CODE_OF_CONDUCT.md).
- For non-trivial changes, open an issue first to discuss the approach.
- Keep PRs focused — one logical change per PR.

## Developer Certificate of Origin (DCO)

Every commit must be signed off to certify you wrote the code or have the right to submit it under Apache 2.0.

Use `git commit -s` — the `-s` flag adds a `Signed-off-by:` trailer. Unsigned commits will fail CI.

See [developercertificate.org](https://developercertificate.org/) for the full DCO text. We deliberately chose DCO over a CLA to keep contributor friction low.

## Development setup

### Prerequisites

- Go 1.23+
- Node.js 20+ (for the UI)
- Docker (for integration tests)
- `make`
- `golangci-lint` (for `make lint`)

### Clone and build

```bash
git clone https://github.com/magpie-project/magpie
cd magpie
make build
```

### Run tests

```bash
make test
```

### Linting

```bash
make lint
```

## Commit style

We use [Conventional Commits](https://www.conventionalcommits.org/). Examples:

- `feat(agent): add postgres preset`
- `fix(control-plane): handle empty OpAMP heartbeat`
- `docs(adr): add ADR 0007 for preset schema`

## Pull request process

1. Fork and branch from `main`.
2. Commit with DCO sign-off (`git commit -s`).
3. Ensure `make test` and `make lint` pass locally.
4. Open a PR against `main`; fill in the PR template.
5. Address review feedback. A maintainer will merge once approved.

## Reporting bugs

File an issue with:

- Magpie version
- OS / platform
- Steps to reproduce
- Expected vs. actual behavior
- Logs (redact secrets!)

## Reporting security issues

See [SECURITY.md](SECURITY.md) — **do not** open a public issue for security reports.

## License

By contributing, you agree your contributions are licensed under the Apache License 2.0 — see [LICENSE](LICENSE).
