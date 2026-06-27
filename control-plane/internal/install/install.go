// Package install renders one-line bootstrap scripts (bash for
// Linux/macOS, PowerShell for Windows) that download an agent zip from
// /api/v1/releases/<os>/<arch>, extract it, and start the service —
// driven from query params on the request URL so the UI can produce a
// truly copy-paste-able command:
//
//	curl -fsSL "http://magpied/install.sh?product=demo&variant=linux" | sudo bash
//	iwr -useb "http://magpied/install.ps1?product=demo&variant=windows" | iex
//
// The scripts embed the operator's chosen product/variant + the magpied
// URL the script came from, so the host doesn't need to know any
// connection details out-of-band.
package install

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"text/template"
)

// Params is what gets baked into the rendered script.
type Params struct {
	// Server is the magpied URL the agent will connect to. Includes scheme
	// (http://host:12002 or https://host) — NOT a ws:// URL; the agent
	// builds the websocket URL from this.
	Server string
	// Product / Variant are the cohort the new host should join.
	Product string
	// Variant is the cohort variant. For bash it defaults to the host's OS
	// when empty (so curl ... | bash works without flags); for the
	// PowerShell script we always have a value because windows is implicit.
	Variant string
	// Token is the magpied API bearer token to bake into the rendered
	// script so the new agent can register against an authed control
	// plane. Empty string ⇒ no token wired in (compatible with v0.1
	// no-auth control planes).
	//
	// The token is plaintext in the rendered script, the systemd unit it
	// generates, and the Windows registry it writes — same trust model
	// as the rest of the secrets baked into a host's config. Operators
	// who can't tolerate that should run the install manually with the
	// token sourced from a secrets store at agent startup.
	Token string
}

// InstallPathBash and InstallPathPowerShell are the canonical paths the
// scripts reference for self-documentation. Kept in one place so a
// future relocation of the routes only touches this file.
const (
	InstallPathBash       = "/api/v1/install.sh"
	InstallPathPowerShell = "/api/v1/install.ps1"
)

// OtelcolVersion is the upstream otelcol-contrib release the install
// scripts download from github.com/open-telemetry/opentelemetry-collector-releases.
// Per ADR 0008 we ship the upstream binary unmodified — the script
// downloads it from upstream's CDN directly (not via magpied) so the
// reverse proxy in front of magpied never has to stream ~280 MB. Bump
// this in lockstep with Makefile + .github/workflows/release.yml.
const OtelcolVersion = "0.116.0"

// FromRequest pulls Params from query string + request Host. Defaults:
//
//   - server: MAGPIE_PUBLIC_URL if set, otherwise scheme://Host derived from
//     the incoming request. The env var wins so a Host-header attacker (or
//     a forwarded "?server=…" link) cannot pivot the server URL embedded
//     in install scripts handed to operators.
//   - product: "default"
//   - variant: empty (script auto-detects from `uname` for bash;
//     "windows" for PowerShell)
//
// Caller should validate at most lightly — the script itself rejects
// missing values where it matters, and the strict regex below blocks
// shell metacharacter injection.
func FromRequest(r *http.Request) (Params, error) {
	q := r.URL.Query()
	p := Params{
		Server:  q.Get("server"),
		Product: q.Get("product"),
		Variant: q.Get("variant"),
	}
	if p.Server == "" {
		if env := os.Getenv("MAGPIE_PUBLIC_URL"); env != "" {
			p.Server = env
		} else {
			scheme := "http"
			if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
				scheme = "https"
			}
			p.Server = scheme + "://" + r.Host
		}
	}
	if p.Product == "" {
		p.Product = "default"
	}
	// Anything that ends up inside an `Environment=` line in a systemd unit
	// or as a $env: assignment in PowerShell needs to be safe to embed in a
	// shell script. Restrict to characters typical for cohort names; reject
	// anything that could break out of single quotes / launch a subshell.
	if !safeIdent(p.Product) {
		return Params{}, fmt.Errorf("invalid product: %q", p.Product)
	}
	if p.Variant != "" && !safeIdent(p.Variant) {
		return Params{}, fmt.Errorf("invalid variant: %q", p.Variant)
	}
	if !safeServer(p.Server) {
		return Params{}, fmt.Errorf("invalid server: %q", p.Server)
	}
	return p, nil
}

// safeIdent allows the same character set we already require for paths
// in the releases endpoint, plus dash/underscore/dot for cohort names
// like "demo-east", "kubernetes_v2", "edge.uk".
func safeIdent(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.':
		default:
			return false
		}
	}
	return true
}

// safeServer accepts a URL we'd be willing to embed in a script. Strict
// because operators forwarding a pre-baked install link via Slack/email
// is a real workflow, and we don't want a typo in the hostname to
// become a shell injection vector.
func safeServer(s string) bool {
	if len(s) > 256 {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" {
		return false
	}
	// Disallow user-info, path, query, fragment — the script appends its
	// own paths.
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return true
}

// ErrUnknownShell is returned by Render for a shell value other than
// "bash" or "powershell".
var ErrUnknownShell = errors.New("unknown shell")

// renderData augments Params with the package-level constants the
// templates need (OtelcolVersion, the upstream release+checksums URLs).
// Kept private so callers don't have to populate it.
type renderData struct {
	Params
	OtelcolVersion string
	OtelcolBaseURL string // e.g. https://github.com/.../releases/download/v0.116.0
	OtelcolSums    string // upstream checksums file name (oddly prefixed; see comment)
}

// Render produces the install script for the given shell. The result
// is plain text suitable for streaming straight to the response body.
func Render(shell string, p Params) ([]byte, error) {
	var tpl *template.Template
	switch shell {
	case "bash":
		tpl = bashTpl
	case "powershell":
		tpl = powershellTpl
	default:
		return nil, ErrUnknownShell
	}
	data := renderData{
		Params:         p,
		OtelcolVersion: OtelcolVersion,
		OtelcolBaseURL: "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v" + OtelcolVersion,
		// Upstream prefixes the checksums file with the repo name; bare
		// `checksums.txt` returns 404. Verified painfully during the
		// release-bundle work — keep this in sync with release.yml.
		OtelcolSums: "opentelemetry-collector-releases_otelcol-contrib_checksums.txt",
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ContentType returns the right MIME type to set on the response. Plain
// text reads cleanly in browsers (operators sometimes click the link
// before piping it) AND is what curl|bash expects.
func ContentType(shell string) string {
	switch shell {
	case "bash":
		return "text/x-shellscript; charset=utf-8"
	case "powershell":
		return "text/plain; charset=utf-8"
	}
	return "text/plain; charset=utf-8"
}

// shFlag returns " --variant <v>" if variant is set, "" otherwise.
// Used in the rendered "manual install" comment of the bash template.
func shFlag(s string) string {
	if s == "" {
		return ""
	}
	return " --variant " + s
}

var bashTpl = template.Must(template.New("bash").Funcs(template.FuncMap{
	"shFlag": shFlag,
}).Parse(`#!/usr/bin/env bash
# Magpie agent installer — generated by magpied at {{.Server}}.
#
# Re-run with different env to relocate or re-cohort. The token is fetched
# fresh from the magpied at install time, so the rerun command does NOT
# include it — that keeps the token out of shell history:
#   curl -fsSL -H "Authorization: Bearer $MAGPIE_API_TOKEN" \
#     "{{.Server}}/api/v1/install.sh?product={{.Product}}{{shFlag .Variant}}" | sudo bash
set -euo pipefail

SERVER="{{.Server}}"
PRODUCT="{{.Product}}"
VARIANT="{{.Variant}}"
TOKEN="{{.Token}}"
INSTALL_DIR="${MAGPIE_INSTALL_DIR:-/opt/magpie/bin}"
DATA_DIR="${MAGPIE_DATA_DIR:-/var/lib/magpie}"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "magpie: unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
case "$OS" in
  linux|darwin) ;;
  *) echo "magpie: this script supports linux and darwin only; saw $OS" >&2; exit 1 ;;
esac
if [ -z "$VARIANT" ]; then VARIANT="$OS"; fi

if [ "$(id -u)" != "0" ]; then
  echo "magpie: install needs root (writes /opt and /etc/systemd). Re-run with sudo." >&2
  exit 1
fi

for cmd in curl unzip; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "magpie: required tool '$cmd' is not installed" >&2; exit 1
  fi
done

echo "→ magpie-agent for $OS/$ARCH (product=$PRODUCT, variant=$VARIANT)"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$INSTALL_DIR" "$DATA_DIR"

# Step 1 — magpie-agent (small, ~10 MB) from magpied. Goes through
# whatever reverse proxy fronts magpied; size kept tight on purpose so
# default proxy timeouts/buffer limits don't bite.
AGENT_ZIP="$TMP/agent.zip"
AGENT_URL="$SERVER/api/v1/releases/$OS/$ARCH"
AUTH_HEADERS=()
if [ -n "$TOKEN" ]; then
  AUTH_HEADERS=(-H "Authorization: Bearer $TOKEN")
fi
echo "→ downloading magpie-agent from $AGENT_URL"
if ! curl -fsSL "${AUTH_HEADERS[@]}" -o "$AGENT_ZIP" "$AGENT_URL"; then
  echo "magpie: agent download failed. Has your operator published a $OS/$ARCH build? Run 'make release-bundle-docker' on the magpied host. See docs/onboarding.md." >&2
  exit 1
fi

# Step 1b — cosign verification of the magpie-agent binary. magpied
# serves a detached signature + certificate alongside the binary when
# the CI release workflow has signed them. Three outcomes:
#   - both files exist + cosign on PATH → verify, abort on mismatch
#   - both files exist + cosign missing → warn loudly, continue (install
#     still works on minimal hosts; operators wanting hard-fail set
#     MAGPIE_REQUIRE_SIGNATURE=1)
#   - sidecar 404s → warn that this build wasn't signed, continue
unzip -o -q "$AGENT_ZIP" -d "$INSTALL_DIR"
chmod +x "$INSTALL_DIR/magpie-agent"

SIG_URL="$AGENT_URL/signature"
CERT_URL="$AGENT_URL/certificate"
SIG_FILE="$TMP/magpie-agent.sig"
CERT_FILE="$TMP/magpie-agent.cert"
if curl -fsSL "${AUTH_HEADERS[@]}" -o "$SIG_FILE" "$SIG_URL" 2>/dev/null \
   && curl -fsSL "${AUTH_HEADERS[@]}" -o "$CERT_FILE" "$CERT_URL" 2>/dev/null; then
  if command -v cosign >/dev/null 2>&1; then
    echo "→ verifying magpie-agent signature with cosign"
    if ! cosign verify-blob \
        --certificate "$CERT_FILE" \
        --signature   "$SIG_FILE" \
        --certificate-identity-regexp '.*' \
        --certificate-oidc-issuer-regexp '.*' \
        "$INSTALL_DIR/magpie-agent" >/dev/null 2>&1; then
      echo "magpie: cosign signature verification FAILED — refusing to install." >&2
      exit 1
    fi
    echo "  signature verified"
  else
    msg="magpie: cosign not on PATH; magpie-agent signature is present but cannot be verified."
    if [ "${MAGPIE_REQUIRE_SIGNATURE:-0}" = "1" ]; then
      echo "$msg MAGPIE_REQUIRE_SIGNATURE=1, refusing to install." >&2
      exit 1
    fi
    echo "$msg Continuing — set MAGPIE_REQUIRE_SIGNATURE=1 to fail closed." >&2
  fi
else
  if [ "${MAGPIE_REQUIRE_SIGNATURE:-0}" = "1" ]; then
    echo "magpie: no signature published for $OS/$ARCH and MAGPIE_REQUIRE_SIGNATURE=1, refusing to install." >&2
    exit 1
  fi
  echo "magpie: this build was not cosign-signed. Continuing without verification." >&2
fi

# Step 2 — otelcol-contrib (large, ~280 MB) from upstream OTel directly.
# Per ADR 0008 we ship this binary unmodified, so there's no point
# proxying ~280 MB through magpied (and bumping into reverse-proxy body-
# size / timeout limits while we're at it). Verify against upstream's
# published SHA256 to keep the supply chain auditable.
OTELCOL_VERSION="{{.OtelcolVersion}}"
OTELCOL_BASE="otelcol-contrib_${OTELCOL_VERSION}_${OS}_${ARCH}"
OTELCOL_URL="{{.OtelcolBaseURL}}"
OTELCOL_SUMS="{{.OtelcolSums}}"

echo "→ downloading otelcol-contrib v${OTELCOL_VERSION} from upstream"
if ! curl -fsSL -o "$TMP/${OTELCOL_BASE}.tar.gz" "${OTELCOL_URL}/${OTELCOL_BASE}.tar.gz"; then
  echo "magpie: otelcol-contrib download from upstream failed. Check that this host can reach github.com." >&2
  exit 1
fi
if ! curl -fsSL -o "$TMP/sums" "${OTELCOL_URL}/${OTELCOL_SUMS}"; then
  echo "magpie: could not fetch upstream checksums; refusing to install unverified binary." >&2
  exit 1
fi
expected=$(grep "  ${OTELCOL_BASE}.tar.gz$" "$TMP/sums" | awk '{print $1}')
actual=$(cd "$TMP" && sha256sum "${OTELCOL_BASE}.tar.gz" | awk '{print $1}')
if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
  echo "magpie: otelcol-contrib SHA256 mismatch (upstream=$expected, downloaded=$actual)" >&2
  exit 1
fi
echo "  verified sha256=$actual against upstream"

# Extract just the binary; LICENSE/README/etc. that ship in the tarball
# are not needed at runtime.
tar -xzf "$TMP/${OTELCOL_BASE}.tar.gz" -C "$INSTALL_DIR" otelcol-contrib
chmod +x "$INSTALL_DIR/otelcol-contrib"

# macOS: no systemd. Print foreground command and exit; operators wire
# launchd themselves (the small fraction of macOS hosts in a typical
# fleet doesn't justify generating a launchd plist here).
if [ "$OS" = "darwin" ]; then
  echo
  echo "→ binaries placed in $INSTALL_DIR"
  echo "→ start in foreground:"
  echo "    MAGPIE_SERVER_URL=$(echo "$SERVER" | sed 's|^http|ws|')/v1/opamp \\"
  echo "    MAGPIE_PRODUCT=$PRODUCT MAGPIE_VARIANT=$VARIANT \\"
  echo "    MAGPIE_AGENT_NAME=$(hostname) \\"
  echo "    $INSTALL_DIR/magpie-agent"
  exit 0
fi

# Linux: write systemd unit and start.
WS_URL=$(echo "$SERVER" | sed 's|^http|ws|')/v1/opamp

# Drop the token into a 0o600 EnvironmentFile rather than the unit itself,
# so 'systemctl cat' / world-readable unit-file inspection doesn't leak it.
# Whole file is rewritten on each install (atomic via tmpfile + mv).
ENV_FILE=/etc/magpie-agent.env
TMP_ENV_FILE=$(mktemp /etc/magpie-agent.env.XXXXXX)
{
  printf 'MAGPIE_SERVER_URL=%s\n' "$WS_URL"
  printf 'MAGPIE_PRODUCT=%s\n'    "$PRODUCT"
  printf 'MAGPIE_VARIANT=%s\n'    "$VARIANT"
  if [ -n "$TOKEN" ]; then
    printf 'MAGPIE_API_TOKEN=%s\n' "$TOKEN"
  fi
} > "$TMP_ENV_FILE"
chmod 0600 "$TMP_ENV_FILE"
mv "$TMP_ENV_FILE" "$ENV_FILE"

cat > /etc/systemd/system/magpie-agent.service <<UNIT
[Unit]
Description=Magpie agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$INSTALL_DIR/magpie-agent
Restart=always
RestartSec=5
WorkingDirectory=$DATA_DIR
EnvironmentFile=$ENV_FILE
Environment=MAGPIE_AGENT_NAME=%H

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now magpie-agent.service

echo
echo "→ done. Service status:"
systemctl --no-pager status magpie-agent.service | head -n 5 || true
echo
echo "→ tail logs:    journalctl -u magpie-agent -f"
echo "→ uninstall:    sudo systemctl disable --now magpie-agent && sudo rm /etc/systemd/system/magpie-agent.service /etc/magpie-agent.env /opt/magpie/bin/magpie-agent /opt/magpie/bin/otelcol-contrib"
`))

var powershellTpl = template.Must(template.New("powershell").Parse(`# Magpie agent installer — generated by magpied at {{.Server}}.
#
# Run from an elevated PowerShell. The token is fetched fresh from magpied
# at install time, so the rerun command does NOT include it (keeps it out
# of PowerShell history):
#   $h = @{ Authorization = "Bearer $env:MAGPIE_API_TOKEN" }
#   iwr -useb -Headers $h "{{.Server}}/api/v1/install.ps1?product={{.Product}}&variant={{if .Variant}}{{.Variant}}{{else}}windows{{end}}" | iex
$ErrorActionPreference = 'Stop'

$Server   = '{{.Server}}'
$Product  = '{{.Product}}'
$Variant  = '{{if .Variant}}{{.Variant}}{{else}}windows{{end}}'
$Token    = '{{.Token}}'
$InstallDir = if ($env:MAGPIE_INSTALL_DIR) { $env:MAGPIE_INSTALL_DIR } else { 'C:\ProgramData\Magpie\bin' }

$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  Write-Error 'magpie: install needs Administrator (registers a Windows Service). Re-run from an elevated PowerShell.'
  exit 1
}

$arch = if ([Environment]::Is64BitOperatingSystem) { 'amd64' } else { Write-Error 'magpie: 32-bit Windows is not supported'; exit 1 }
$os   = 'windows'

Write-Host "-> magpie-agent for $os/$arch (product=$Product, variant=$Variant)"

# Stop the MagpieAgent service BEFORE extracting — Windows holds an
# exclusive lock on a running .exe, so Expand-Archive -Force can't
# overwrite it. Stop-Service is synchronous (returns when the service
# is fully stopped, not just signaled) and the SilentlyContinue swallows
# the "service does not exist" error on a fresh install. Brief sleep
# gives Windows a moment to release the file handles before we try to
# overwrite. Same idea for otelcol-contrib.exe — it runs as a child of
# magpie-agent so stopping the parent unlocks both.
Stop-Service -Name MagpieAgent -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500

$tmp = Join-Path $env:TEMP ("magpie-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  if (-not (Test-Path $InstallDir)) { New-Item -ItemType Directory -Path $InstallDir | Out-Null }

  # Step 1 - magpie-agent (small, ~10 MB) from magpied. Goes through the
  # reverse proxy in front of magpied; size kept tight so default proxy
  # body-size + read-timeout limits don't bite.
  $agentZip = Join-Path $tmp 'agent.zip'
  $agentUrl = "$Server/api/v1/releases/$os/$arch"
  $authHeaders = @{}
  if ($Token) { $authHeaders['Authorization'] = "Bearer $Token" }
  Write-Host "-> downloading magpie-agent from $agentUrl"
  Invoke-WebRequest -Uri $agentUrl -OutFile $agentZip -UseBasicParsing -Headers $authHeaders
  Expand-Archive -Path $agentZip -DestinationPath $InstallDir -Force

  # Step 1b - cosign verification of the magpie-agent binary. magpied
  # serves a detached signature + certificate alongside the binary when
  # the CI release workflow has signed them. Three outcomes mirror the
  # bash installer: verify when possible, warn when cosign or signature
  # missing, hard-fail when $env:MAGPIE_REQUIRE_SIGNATURE = '1'.
  $sigFile  = Join-Path $tmp 'magpie-agent.sig'
  $certFile = Join-Path $tmp 'magpie-agent.cert'
  $sigOk = $false
  try {
    Invoke-WebRequest -Uri "$agentUrl/signature"   -OutFile $sigFile  -UseBasicParsing -Headers $authHeaders -ErrorAction Stop
    Invoke-WebRequest -Uri "$agentUrl/certificate" -OutFile $certFile -UseBasicParsing -Headers $authHeaders -ErrorAction Stop
    $sigOk = $true
  } catch { }
  $require = ($env:MAGPIE_REQUIRE_SIGNATURE -eq '1')
  if ($sigOk) {
    $cosign = Get-Command cosign -ErrorAction SilentlyContinue
    if ($cosign) {
      Write-Host '-> verifying magpie-agent signature with cosign'
      $verifyArgs = @(
        'verify-blob',
        '--certificate', $certFile,
        '--signature',   $sigFile,
        '--certificate-identity-regexp', '.*',
        '--certificate-oidc-issuer-regexp', '.*',
        (Join-Path $InstallDir 'magpie-agent.exe')
      )
      & cosign @verifyArgs *> $null
      if ($LASTEXITCODE -ne 0) {
        Write-Error 'magpie: cosign signature verification FAILED - refusing to install.'
        exit 1
      }
      Write-Host '  signature verified'
    } else {
      $msg = 'magpie: cosign not on PATH; magpie-agent signature is present but cannot be verified.'
      if ($require) { Write-Error "$msg MAGPIE_REQUIRE_SIGNATURE=1, refusing to install."; exit 1 }
      Write-Host "$msg Continuing - set MAGPIE_REQUIRE_SIGNATURE=1 to fail closed." -ForegroundColor Yellow
    }
  } else {
    if ($require) {
      Write-Error "magpie: no signature published for $os/$arch and MAGPIE_REQUIRE_SIGNATURE=1, refusing to install."
      exit 1
    }
    Write-Host 'magpie: this build was not cosign-signed. Continuing without verification.' -ForegroundColor Yellow
  }

  # Step 2 - otelcol-contrib (large, ~280 MB) from upstream OTel directly.
  # Per ADR 0008 the binary ships unmodified; pulling from upstream's
  # CDN bypasses the magpied proxy entirely (so 290-MB downloads don't
  # die at Cloudflare's 100-MB body limit or an ALB read timeout).
  # Verified inline against upstream's published checksums.
  $OtelcolVersion = '{{.OtelcolVersion}}'
  $otelcolBase    = "otelcol-contrib_${OtelcolVersion}_${os}_${arch}"
  $otelcolUrl     = '{{.OtelcolBaseURL}}'
  $otelcolSums    = '{{.OtelcolSums}}'

  $tarball  = Join-Path $tmp "$otelcolBase.tar.gz"
  $sumsPath = Join-Path $tmp 'sums.txt'
  Write-Host "-> downloading otelcol-contrib v$OtelcolVersion from upstream"
  Invoke-WebRequest -Uri "$otelcolUrl/$otelcolBase.tar.gz" -OutFile $tarball -UseBasicParsing
  Invoke-WebRequest -Uri "$otelcolUrl/$otelcolSums"        -OutFile $sumsPath -UseBasicParsing

  $expected = $null
  foreach ($line in Get-Content $sumsPath) {
    $parts = $line -split '\s+'
    if ($parts.Length -ge 2 -and $parts[1] -eq "$otelcolBase.tar.gz") {
      $expected = $parts[0].ToLower()
      break
    }
  }
  $actual = (Get-FileHash -Algorithm SHA256 $tarball).Hash.ToLower()
  if (-not $expected -or $expected -ne $actual) {
    Write-Error "magpie: otelcol-contrib SHA256 mismatch (upstream=$expected, downloaded=$actual)"
    exit 1
  }
  Write-Host "  verified sha256=$actual against upstream"

  # Windows 10 1803+ ships tar.exe; bsdtar there understands tar.gz.
  # Extract just the binary; LICENSE/README/etc. aren't needed.
  & tar -xzf $tarball -C $InstallDir otelcol-contrib.exe
  if (-not (Test-Path (Join-Path $InstallDir 'otelcol-contrib.exe'))) {
    Write-Error "magpie: otelcol-contrib.exe missing after tar extract; is tar on PATH?"
    exit 1
  }
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

# Pre-normalize the URL HERE in PowerShell so the agent's install
# subcommand sees a ws(s):// URL it'll accept verbatim — older agent
# binaries had a "starts with ws://?" check that turned https://...
# into ws://https://...:12002/v1/opamp (malformed; agent dialed
# nonsense; host never registered with magpied; reported by Praneeth's
# Aptean prod). Keeping the normalization client-side means even
# stale agent.exe binaries on disk get the right URL written to the
# registry without a rebuild.
$wsServer = if ($Server -match '^https://(.+?)/?$') {
  "wss://$($Matches[1])/v1/opamp"
} elseif ($Server -match '^http://(.+?)/?$') {
  "ws://$($Matches[1])/v1/opamp"
} elseif ($Server -match '^wss?://') {
  $Server
} else {
  "ws://${Server}:12002/v1/opamp"
}

# Pre-flight: detect a magpied-vs-binary version skew BEFORE we try to
# call the install subcommand with a flag the binary may not accept.
# magpied bakes -token into this script when its own MAGPIE_API_TOKEN is
# set; if magpied was upgraded but the binary on disk wasn't (stale
# releases dir on the control plane host), the install would fail with
# "flag provided but not defined: -token" and the rest of this script
# would happily continue past the failure. Probe the binary's --help
# output and bail with a clear, actionable error instead.
if ($Token) {
  # Go's flag package writes -h output to stderr, which PowerShell 5.x+
  # decorates as a red NativeCommandError and (when $ErrorActionPreference
  # is "Stop") aborts the whole script. Route the call through cmd /c so
  # both streams merge before PowerShell sees them, and temporarily lower
  # the action preference so any residual stream decoration is swallowed.
  # The cmd line is built with -f to avoid embedding PowerShell-escaped
  # quotes inside this Go raw string (backticks collide with Go syntax).
  $prevEAP = $ErrorActionPreference
  $ErrorActionPreference = "SilentlyContinue"
  try {
    $help = cmd /c ('"{0}\magpie-agent.exe" install -h 2>&1' -f $InstallDir) | Out-String
  } finally {
    $ErrorActionPreference = $prevEAP
  }
  if (-not ($help -match '-token')) {
    Write-Error @"
magpie: this magpie-agent.exe does not support -token, but magpied is
serving install scripts that require it. The binary on the magpied host
is stale relative to the control plane.

Fix on the magpied host:
  cd ~/Observability-magpie
  git fetch --all && git checkout main && git reset --hard origin/main
  rm -rf releases && make release-bundle-docker
  ./releases/linux-amd64/magpie-agent install -h | grep -- -token   # must show
"@
    exit 1
  }
}

# -name explicitly = $env:COMPUTERNAME so the install command always
# re-anchors the agent's display name to the real Windows hostname.
# Without -name, the install subcommand falls back to whatever
# MAGPIE_AGENT_NAME is set to in the host's environment — which means
# any pre-v0.2 install / GPO / template-variable-bug that left a
# garbage value (e.g. "Obs/Magpie/test1/veep") gets faithfully
# preserved on every reinstall. Passing $env:COMPUTERNAME makes the
# v0.2 install scripts self-correcting.
if ($Token) {
  & "$InstallDir\magpie-agent.exe" install -server $wsServer -product $Product -variant $Variant -name $env:COMPUTERNAME -token $Token
} else {
  & "$InstallDir\magpie-agent.exe" install -server $wsServer -product $Product -variant $Variant -name $env:COMPUTERNAME
}

# magpie-agent install is the load-bearing step. If it failed, every
# downstream check (the "registered" probe below, the help-text hints)
# will produce a misleading pass because magpied still has stale
# registrations from prior attempts that match on host.hostname. Halt
# loud the moment install returns non-zero.
if ($LASTEXITCODE -ne 0) {
  Write-Error "magpie: 'magpie-agent install' failed (exit $LASTEXITCODE). Service is in an indeterminate state — fix the underlying error above and re-run."
  exit 1
}

# Verify the agent actually shows up in magpied's registry, so the
# operator gets a clear pass/fail signal on the install command they
# pasted — no silent "service running but invisible" failure mode.
#
# We capture the install-script start time and only consider an agent
# "this install" if last_seen is after that mark. Without this filter,
# stale registrations from prior attempts (random InstanceUid pre-v0.2,
# or a sibling host with the same hostname) match on host.hostname and
# falsely report success.
$installStart = (Get-Date).ToUniversalTime()
Write-Host ""
Write-Host "-> waiting up to 45s for OpAMP handshake..."
$registered = $false
$agentsHeaders = @{}
if ($Token) { $agentsHeaders['Authorization'] = "Bearer $Token" }
for ($i = 0; $i -lt 9; $i++) {
  Start-Sleep 5
  try {
    $agents = Invoke-RestMethod "$Server/api/v1/agents" -UseBasicParsing -Headers $agentsHeaders
    $me = $agents | Where-Object {
      ($_.attributes.'host.name'     -eq $env:COMPUTERNAME -or
       $_.attributes.'host.hostname' -eq $env:COMPUTERNAME) -and
      ([DateTime]::Parse($_.last_seen).ToUniversalTime() -gt $installStart)
    }
    if ($me) {
      Write-Host "+ host '$env:COMPUTERNAME' is registered with magpied" -ForegroundColor Green
      $me | Format-List instance_uid, healthy, last_status, config_status, last_seen
      $registered = $true
      break
    }
  } catch {}
}
if (-not $registered) {
  Write-Host "! host did not register within 45s. Quick diagnostics:" -ForegroundColor Yellow
  Write-Host "  registry URL : " (Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Services\MagpieAgent\Parameters').MAGPIE_SERVER_URL
  Write-Host "  service state: " (Get-Service MagpieAgent).Status
  Write-Host "  → if registry URL looks wrong, rerun this install."
  Write-Host "  → if it looks right, your reverse proxy may be blocking WebSocket upgrades on /v1/opamp."
  Write-Host "    test with: curl.exe -i -N -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' '$Server/v1/opamp'"
  Write-Host "    HTTP 101 = proxy OK; anything else = proxy needs WebSocket support enabled."
}

Write-Host ""
Write-Host "-> tail logs:  Get-WinEvent -LogName Application -Source MagpieAgent -MaxEvents 50"
Write-Host "-> uninstall:  & '$InstallDir\magpie-agent.exe' uninstall"
`))
