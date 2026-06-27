# Security Policy

## Reporting a vulnerability

**Please do not file public GitHub issues for security vulnerabilities.**

Email security reports to **security@magpie-project.io** _(placeholder — to be updated)_.

Include:

- A description of the issue
- Steps to reproduce, or a proof of concept
- Affected versions
- Any mitigations you've identified

We will acknowledge receipt within **3 business days** and send a detailed response within **10 business days** with next steps.

## Supported versions

Magpie is in pre-alpha. Once we cut a stable release, we will support the latest minor version and the one prior.

## Disclosure process

1. We confirm the issue and assign a CVSS 3.1 severity.
2. We develop a fix in a private branch.
3. We coordinate a disclosure date with the reporter.
4. We release the fix and publish an advisory on release day.
5. Credit is given to the reporter unless anonymity is requested.

## Authentication and the trust model

Magpie v0.2 ships a single shared API token (`MAGPIE_API_TOKEN`) that authorizes every REST call and every OpAMP WebSocket upgrade. This is the **v0.2 MVP** — the right gate to keep "anyone on the network can RCE every host" from being trivially achievable, and the wrong gate for any setting where individual operators should have distinct, revocable identities. Per-agent enrollment tokens and OIDC for operators are scheduled for v0.3.

If a token leaks:

1. Generate a new token (`openssl rand -base64 32`).
2. Set `MAGPIE_API_TOKEN=<new>` on `magpied`, restart.
3. Re-run `magpie-agent install -token <new> ...` on each host (or rewrite `/etc/magpie-agent.env` / the registry `Parameters` key directly).
4. Tell each operator the new token; their UI session will surface a sign-in prompt automatically on the next poll.

Until that's done, the leaked token is full control-plane access.

The shared-token model also means: anyone on a host with the agent installed can read the token (it lives in the host's environment / systemd EnvironmentFile / Windows registry). Treat any agent host as a credentials-bearing host — same trust posture as a host with an SSH key for the control plane.

## Supply-chain integrity

All release artifacts are:

- Signed with [Sigstore cosign](https://www.sigstore.dev/)
- Accompanied by an [SBOM](https://www.cisa.gov/sbom) produced by `syft`
- Built with [SLSA v1](https://slsa.dev/) provenance attestations
