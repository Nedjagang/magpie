# Packaging

Build artifacts for each supported platform.

| Directory | Format | Status |
|---|---|---|
| `docker/` | Dockerfile + compose for `magpied` and the UI | ✅ shipped in v0.1 |
| `deb/` | `.deb` (planned, via `nfpm`) for Debian/Ubuntu | ⏳ planned |
| `rpm/` | `.rpm` (planned, via `nfpm`) for RHEL/Fedora/Rocky/Alma | ⏳ planned |
| `msi/` | `.msi` (planned, via WiX v5) for Windows | ⏳ planned |
| `helm/` | Helm chart for Kubernetes | ⏳ planned |

## What's shipped

**`docker/`** — build and run the control plane + UI with:

```bash
# from repo root
docker compose -f packaging/docker/docker-compose.yml up -d
```

Or via the Makefile:

```bash
make docker       # build the images
make docker-up    # start the compose stack
make docker-down  # stop it
```

## Future releases

Planned release artifacts will include:

- **Signed** with [Sigstore cosign](https://www.sigstore.dev/)
- Accompanied by an **SBOM** (`syft`)
- Built with **[SLSA v1](https://slsa.dev/)** provenance attestations
- Multi-arch (`linux/amd64`, `linux/arm64`, `windows/amd64`)

Release orchestration will live in `.github/workflows/release.yml` once we tag v0.2.
