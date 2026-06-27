# Magpie Helm Chart

Deploys the Magpie control plane (`magpied`) and UI to a Kubernetes cluster. Agents still run on the hosts they monitor — see [docs/onboarding.md](../../../docs/onboarding.md); this chart does not install agents.

## Install

```bash
# From the repo root
helm install magpie packaging/helm/magpie \
  --namespace magpie --create-namespace \
  --set magpied.image.repository=your-registry.example.com/magpie/magpied \
  --set ui.image.repository=your-registry.example.com/magpie/ui
```

The default image repositories (`magpie/magpied`, `magpie/ui`) are placeholders — no public image is published yet. Either build locally (`make docker`) and load into your cluster, or push to a registry your nodes can pull from.

## Values worth tweaking

| Value | What it does |
|---|---|
| `magpied.image.repository` / `.tag` | Which magpied image to pull. Tag defaults to `Chart.appVersion` when empty. |
| `magpied.persistence.size` | SQLite PVC size. 1Gi holds years of configs + audit for a mid-sized fleet. |
| `magpied.releasesPersistence.enabled` | If true, mounts a second PVC at `/releases` to serve agent binary downloads via the UI's install flow. Disable if you host binaries elsewhere. |
| `ui.enabled` | Set to `false` if you only use the REST API and don't want the dashboard pod. |
| `ingress.enabled` | Fronts both UI and magpied behind one hostname, same-origin. Off by default — use `kubectl port-forward` during bring-up. |

## What's NOT in this chart

- **Agent DaemonSet.** Agents install on hosts via the onboarding flow — no container image is published yet for the Kubernetes case.
- **Multi-replica magpied.** SQLite/WAL is single-writer; replicas would fight for the PVC lock. Switch to Postgres (ADR 0007) first if you need HA.
- **NetworkPolicy.** Add your own — magpied needs inbound from agents (OpAMP port 12002) and from the UI Service.
- **Prometheus scrape annotations.** magpied doesn't expose `/metrics` yet; adding it is on the roadmap.

## Uninstall

```bash
helm uninstall magpie -n magpie
```

This leaves the PVCs behind intentionally — deleting them would destroy every config and audit record. Clean up with `kubectl -n magpie delete pvc -l app.kubernetes.io/instance=magpie` if you want a fresh slate.
