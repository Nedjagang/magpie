# Onboarding a host

Connecting a new machine to a running Magpie control plane takes about two minutes:

1. Download one zip
2. Copy its contents somewhere stable
3. Set four environment variables
4. Run `magpie-agent.exe install`

That's it — the agent installs itself as a Windows Service and starts.

---

## Prerequisites

- You know the `magpied` server address. Example: `34.203.177.194`.
- You know which **product** and **variant** this host should join (e.g. `observability` / `windows`). A config for that pair should already exist in the UI — otherwise the agent connects but stays idle.
- The host can reach `TCP/12002` on the `magpied` server (outbound only; no inbound ports on the host).
- You have an **elevated PowerShell** (Run as Administrator).

---

## Windows

### 1. Download

Visit the v0.2.0 release page and grab the zip:

**https://github.com/Aptean-Internal/Observability-magpie/releases/tag/v0.2.0**

Download `magpie-agent-windows-amd64-v0.2.0.zip` (~32 MB).

> The repo is private. GitHub will prompt you to sign in if you aren't already. Use your Aptean GitHub account.

### 2. Extract

Right-click the zip → **Extract All…** → pick a location. Recommended:

```
C:\ProgramData\Magpie\bin
```

After extraction the folder contains:

- `magpie-agent.exe` — the supervisor. This is what you run.
- `otelcol-contrib.exe` — the upstream OpenTelemetry Collector (unmodified; see [ADR 0008](adr/0008-upstream-otelcol-contrib.md)). The agent spawns this for you; don't run it directly. Its SHA256 in our `SHA256SUMS` matches the one published at [opentelemetry-collector-releases](https://github.com/open-telemetry/opentelemetry-collector-releases/releases), feel free to verify.
- `README.txt` — short recap of these steps.

Both exes must live in the same directory.

### 3. Install as a Windows Service

Open **PowerShell as Administrator**, then:

```powershell
cd C:\ProgramData\Magpie\bin
.\magpie-agent.exe install `
    -server  34.203.177.194 `
    -product observability `
    -variant windows
```

Replace the values to match your cohort. The install command:
- Writes `MAGPIE_SERVER_URL` / `MAGPIE_PRODUCT` / `MAGPIE_VARIANT` / `MAGPIE_AGENT_NAME` at **Machine scope** (so the LocalSystem service sees them).
- Registers the `MagpieAgent` service with SCM.
- Starts it.

Expected output:
```
→ configured at Machine scope:
    MAGPIE_SERVER_URL = ws://34.203.177.194:12002/v1/opamp
    MAGPIE_PRODUCT    = observability
    MAGPIE_VARIANT    = windows
    MAGPIE_AGENT_NAME = <your-hostname>
✓ MagpieAgent service installed and started
```

The agent now runs under SCM, starts automatically at boot, and reconnects on its own if the network or control plane blip.

### Change configuration later

Re-run `install` with the new flags — it's idempotent and updates the Machine env vars + restarts the service:

```powershell
.\magpie-agent.exe install -server <new-host> -product <new> -variant <new>
```

### Why flags instead of $env:

`$env:MAGPIE_FOO = "..."` sets a value only in your current PowerShell session. Services don't see it. The install flags above bypass that trap by writing to Machine-scope directly. If you've set Machine-scope vars previously and want to keep them, `install` with no flags falls back to reading them from the environment — so you only need flags the first time or when changing values.

### Verify

```powershell
# from any PowerShell
.\magpie-agent.exe status
# → MagpieAgent: running (PID 12345)

# or via the OS:
sc.exe query MagpieAgent

# recent log entries from the service:
Get-EventLog -LogName Application -Source MagpieAgent -Newest 10
```

In the Magpie UI (`http://<magpied-host>:12001`), click your product in the sidebar — your host should appear in the **Hosts** table within a few seconds, healthy, `config_status = applied`.

### Uninstall

```powershell
cd C:\ProgramData\Magpie\bin
.\magpie-agent.exe uninstall
```

Removes the service. Binaries stay — delete the folder if you want a full cleanup.

---

## Linux

Linux uses systemd instead of the Windows SCM. The binary has no `install` subcommand on Linux; you write a unit file yourself.

Linux builds are not in the v0.2.0 release yet — build from source:

```bash
git clone https://github.com/Aptean-Internal/Observability-magpie.git
cd Observability-magpie
make build            # builds magpie-agent
make fetch-collector  # downloads + verifies upstream otelcol-contrib
sudo install -D -m 0755 bin/magpie-agent     /opt/magpie/bin/magpie-agent
sudo install -D -m 0755 bin/otelcol-contrib  /opt/magpie/bin/otelcol-contrib
```

Then:

```bash
sudo mkdir -p /var/lib/magpie
sudo tee /etc/systemd/system/magpie-agent.service >/dev/null <<'EOF'
[Unit]
Description=Magpie agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/opt/magpie/bin/magpie-agent
Restart=always
RestartSec=5
WorkingDirectory=/var/lib/magpie
Environment=MAGPIE_SERVER_URL=ws://34.203.177.194:12002/v1/opamp
Environment=MAGPIE_PRODUCT=observability
Environment=MAGPIE_VARIANT=linux
Environment=MAGPIE_AGENT_NAME=%H

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now magpie-agent
journalctl -u magpie-agent -f
```

Pre-built Linux binaries will land in a later release — track the repo for updates.

---

## Running in foreground (for testing, no service)

Sometimes you want to see the logs live instead of going through Event Viewer:

```powershell
cd C:\ProgramData\Magpie\bin
.\magpie-agent.exe
```

Ctrl+C to stop. This is the same binary — just without the `install` subcommand, it runs interactively.

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| `install failed: Access is denied.` | PowerShell isn't elevated. Re-open as Administrator. |
| Service installs but stops within 30s | Control plane unreachable. Check `Test-NetConnection <server> -Port 12002` and that MAGPIE_SERVER_URL is set at Machine scope. |
| Host appears in UI as unhealthy or with an error | Click the host row in the UI — last error is shown in the drawer. Almost always a YAML typo in the published config. |
| Hash column says "—" for the host | No config exists for this host's (product, variant). Publish one in the UI → the agent picks it up in ~2 seconds. |
| `opamp connect failed` in logs, repeatedly | Firewall between host and magpied, or magpied down. Agent will keep retrying; collector keeps running the last-applied config in the meantime. |
| Want to see the collector's own metrics | `curl http://localhost:8888/metrics` from the host. Not exposed externally. |

---

## What each env var controls

| Variable | Required | Purpose |
|---|---|---|
| `MAGPIE_SERVER_URL` | yes | OpAMP endpoint — `ws://<magpied-host>:12002/v1/opamp` |
| `MAGPIE_PRODUCT` | yes | Which product cohort this host joins |
| `MAGPIE_VARIANT` | yes | Which variant within the product (`windows`, `linux`, `kubernetes`) |
| `MAGPIE_AGENT_NAME` | no | Display name in the UI; defaults to hostname |
| `MAGPIE_AGENT_CONFIG_PATH` | no | Where to cache the applied config; defaults to `magpie-agent-config.yaml` next to the binary |
| `MAGPIE_COLLECTOR_BINARY` | no | Path to `otelcol-contrib.exe`; defaults to the same directory as the agent |
