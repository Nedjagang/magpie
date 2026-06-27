# Magpie

**Remote-manage your OpenTelemetry collectors without writing Ansible.**

Magpie is a control plane for OpenTelemetry. You publish a collector config once; agents on every matching host pick it up within seconds. Configs hot-reload — no host restarts, no rolling deploys. Works on Linux, Windows, and Kubernetes. Sends telemetry to any OTLP-compatible backend (Grafana, Datadog, Honeycomb, Jaeger, your own).

It is deliberately small: no custom config DSL, no forked collector, no vendor lock-in. The collector under each agent is vanilla OpenTelemetry.

> **Status: v0.2 — internal alpha with shared-token auth.** Set `MAGPIE_API_TOKEN` on `magpied` and pass it as `Authorization: Bearer …` from the UI / agents / install scripts. Empty token retains v0.1 no-auth behavior with a startup warning, so upgrades are smooth. Full caveat list: [LIMITATIONS.md](LIMITATIONS.md).

---

## What it does

- **Group hosts by product and variant.** Tag each host with `MAGPIE_PRODUCT=ship MAGPIE_VARIANT=linux` and it joins that cohort. Publish one YAML per cohort; only matching agents receive it.
- **Hot-reload in ~2 seconds.** Publish from the UI → matching agents reload their collector subprocess with the new pipeline. No SSH, no Ansible, no reboots.
- **Rollback with one click.** Every publish is a new revision. Prior revisions are listed; any can be re-applied instantly.
- **Audit log.** Who pushed what, when, against which cohort. Append-only.
- **Per-host label override.** Move a host from `ship/linux` to `pay/linux` from the UI without touching the agent's env vars.
- **Survives control-plane outages.** Agents keep running the last-applied config if Magpie is down. Telemetry does not route through Magpie — agents export OTLP directly to your backend.

## What it is *not*

- **Not a telemetry store.** Magpie doesn't receive, store, or alert on metrics. That's your OTLP backend's job.
- **Not a replacement for your backend.** It's a fleet-management layer *in front of* OpenTelemetry collectors that ship to whatever backend you pick.
- **Not production-ready yet.** See [LIMITATIONS.md](LIMITATIONS.md). Use internally behind a VPN until auth, staged rollouts, and HA land.

---

## Architecture in one picture

```
 ┌──────────────────────┐                 ┌──────────────────────┐
 │  magpied             │                 │  Next.js UI          │
 │  (control plane)     │◀──── REST ─────▶│  (browser / :12001)  │
 │  :12002              │                 └──────────────────────┘
 │                      │
 │  SQLite: configs,    │
 │  audit_log,          │◀── OpAMP (WebSocket) ──────────┐
 │  agent_labels        │                                 │
 └──────────────────────┘                                 │
                                                          ▼
                                         ┌─────────────────────────────┐
                                         │  Host                       │
                                         │  ┌──────────────────────┐   │
                                         │  │  magpie-agent        │   │
                                         │  │  (supervisor)        │   │
                                         │  │       │              │   │
                                         │  │       │ spawns       │   │
                                         │  │       ▼              │   │
                                         │  │  otelcol-contrib     │   │
                                         │  │  (upstream OTel)     │   │
                                         │  └──────────┬───────────┘   │
                                         └─────────────┼───────────────┘
                                                       │
                                                       │ OTLP
                                                       ▼
                                         ┌─────────────────────────────┐
                                         │  Your telemetry backend     │
                                         │  (Grafana, Datadog, …)      │
                                         └─────────────────────────────┘
```

More: [docs/architecture.md](docs/architecture.md).

---

## Quickstart — Docker (5 minutes)

```bash
git clone https://github.com/<your-org>/magpie.git
cd magpie

# Generate a token. Keep it out of shell history when possible (read -s).
export MAGPIE_API_TOKEN=$(openssl rand -base64 32)
echo "MAGPIE_API_TOKEN=$MAGPIE_API_TOKEN" > packaging/docker/.env

docker compose --env-file packaging/docker/.env -f packaging/docker/docker-compose.yml up -d
```

Then:
- UI: `http://localhost:12001` — paste the same token when prompted.
- `magpied` REST + OpAMP: `http://localhost:12002` (still external for agents).

Run an agent against it:

```bash
# Linux/macOS — token in the agent's env so it can authenticate to magpied
export MAGPIE_AGENT_NAME=$(hostname)
export MAGPIE_PRODUCT=demo
export MAGPIE_VARIANT=linux
export MAGPIE_SERVER_URL=ws://localhost:12002/v1/opamp
export MAGPIE_API_TOKEN=...  # same value as on magpied
./bin/magpie-agent
```

```powershell
# Windows
$env:MAGPIE_AGENT_NAME = $env:COMPUTERNAME
$env:MAGPIE_PRODUCT    = "demo"
$env:MAGPIE_VARIANT    = "windows"
$env:MAGPIE_SERVER_URL = "ws://localhost:12002/v1/opamp"
$env:MAGPIE_API_TOKEN  = "..."   # same value as on magpied
.\bin\magpie-agent.exe
```

The host appears in the UI Fleet within seconds. Click **+ New product** → pick a variant → edit YAML → Publish. The agent picks up the new config in ~2 seconds, no restart.

---

## Quickstart — from source

Requires: Go 1.22+, Node 20+, pnpm 9+.

```bash
git clone https://github.com/<your-org>/magpie.git
cd magpie

make build                 # magpied + magpie-agent
make fetch-collector       # downloads + verifies upstream otelcol-contrib
make quickstart            # launches magpied + UI locally
```

Agents you launch separately (env-var recipe above).

---

## Onboarding a host

Minimum environment:

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `MAGPIE_SERVER_URL` | yes | `ws://localhost:12002/v1/opamp` | Where to reach `magpied` |
| `MAGPIE_PRODUCT` | yes | `default` | Product bucket (e.g. `ship`, `pay`) |
| `MAGPIE_VARIANT` | yes | OS family | Sub-grouping (e.g. `linux`, `windows`, `kubernetes`) |
| `MAGPIE_AGENT_NAME` | no | hostname | Display name in UI |
| `MAGPIE_API_TOKEN` | yes¹ | _(unset)_ | Bearer token, must match `magpied`'s `MAGPIE_API_TOKEN` |

¹ Required when targeting a v0.2 control plane that has `MAGPIE_API_TOKEN` set. Omit only when both sides are running v0.1-compatible no-auth.

## Auth (v0.2)

`magpied` reads `MAGPIE_API_TOKEN` at startup. When set, every REST call and every OpAMP WebSocket upgrade must carry `Authorization: Bearer <token>`. The same token is baked into `install.sh` / `install.ps1` output (so `curl|bash` onboarding still works in one paste, given the operator first sets `$MAGPIE_API_TOKEN` in their shell). The UI prompts for the token on first load and stores it in `localStorage`.

Generate a token with `openssl rand -base64 32` (or anything your secrets manager hands you), set it on `magpied`, and rotate by changing the env var on `magpied` and re-running install on each host.

Single shared token is deliberately the v0.2 MVP — per-agent enrollment + OIDC for operators land in v0.3. See [LIMITATIONS.md](LIMITATIONS.md).

## Constraining what configs can do

Auth gates *who* can publish a config. The otelcol component allowlist gates *what* an authed operator can publish. Both matter — a leaked token is far less damaging if the allowlist excludes file readers and arbitrary HTTP scrapers.

Set per-section CSV env vars on `magpied`:

```bash
MAGPIE_ALLOWED_RECEIVERS=otlp,hostmetrics,httpcheck
MAGPIE_ALLOWED_PROCESSORS=batch,memory_limiter,resource,attributes
MAGPIE_ALLOWED_EXPORTERS=otlp,otlphttp,debug
```

A config that references `filelog`, `journald`, or any unlisted component type is rejected at publish time with a clear error. Unset / empty = no enforcement on that section, matching v0.1 behavior so upgrades roll forward without breaking existing configs.

Use `name/instance` (e.g. `otlphttp/datadog`, `otlphttp/grafana`) to run multiple instances of the same allowlisted type — the allowlist gates the type prefix, not the operator-chosen suffix.

**Networking:** host needs outbound `TCP/12002` to magpied and outbound to your OTLP backend. Zero inbound ports on the host.

Full onboarding guide (Windows + Linux, one-command install script): [docs/onboarding.md](docs/onboarding.md).

---

## Repository layout

| Path | Contents |
|---|---|
| `agent/` | Magpie agent (supervisor) + OTel collector builder manifest |
| `control-plane/` | `magpied` — Go service: OpAMP server, REST API, SQLite |
| `ui/` | Next.js 15 dashboard |
| `packaging/docker/` | Dockerfile + compose for the control plane |
| `packaging/{deb,rpm,msi,helm}/` | Planned native packaging — skeleton only in v0.1 |
| `docs/` | Architecture, ADRs |

---

## How it compares

| | Magpie | OpenTelemetry Operator | Grafana Alloy (OpAMP) | Ansible + otelcol |
|---|---|---|---|---|
| Works on Linux & Windows VMs | ✅ | ❌ (k8s only) | ✅ | ✅ |
| Works on Kubernetes | ✅ | ✅ | ✅ | — |
| Hot-reload without rollout | ✅ | via CRD | ✅ | ❌ (10-min playbook) |
| UI for authoring configs | ✅ | ❌ | ✅ (Grafana Cloud) | ❌ |
| Vendor-neutral OTLP | ✅ | ✅ | ✅ | ✅ |
| Backend lock-in | none | none | nudges Grafana Cloud | none |
| Production-ready today | **no** | yes | yes | yes |

Magpie's claim: *"Alloy's mental model, smaller scope, no vendor pressure, self-host in 5 minutes."* It is the right pick if:
- you want hot-reload config management across mixed OS fleets, *and*
- you don't want to commit to Grafana's ecosystem, *and*
- you accept v0.1 is internal-only for now.

---

## Documentation

- [Architecture](docs/architecture.md)
- [Limitations](LIMITATIONS.md) — honest list of what isn't done yet
- [Changelog](CHANGELOG.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)

---

## License

Apache 2.0 — see [LICENSE](LICENSE).
