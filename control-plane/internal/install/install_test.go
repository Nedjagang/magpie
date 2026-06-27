package install_test

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/magpie-project/magpie/control-plane/internal/install"
)

// TestPathsAreUnderAPIv1 pins the canonical install paths to /api/v1 so
// nobody accidentally moves them back to the root. Root-level paths got
// 404'd in production through a reverse proxy that only forwarded
// /api/v1 + /v1/opamp + /healthz. This is a contract test, not a
// behavior test — the strings live in install.go's exported constants
// so the rendered scripts and the UI both stay in sync.
func TestPathsAreUnderAPIv1(t *testing.T) {
	if install.InstallPathBash != "/api/v1/install.sh" {
		t.Errorf("InstallPathBash = %q, want /api/v1/install.sh", install.InstallPathBash)
	}
	if install.InstallPathPowerShell != "/api/v1/install.ps1" {
		t.Errorf("InstallPathPowerShell = %q, want /api/v1/install.ps1", install.InstallPathPowerShell)
	}
}

func TestRenderBashEmbedsParams(t *testing.T) {
	body, err := install.Render("bash", install.Params{
		Server:  "https://magpie.example.com",
		Product: "observability-team",
		Variant: "linux",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(body)
	for _, want := range []string{
		"#!/usr/bin/env bash",
		`SERVER="https://magpie.example.com"`,
		`PRODUCT="observability-team"`,
		`VARIANT="linux"`,
		// Re-run hint must reference the api/v1 path — guards against the
		// production-404 regression we hit when this lived at the root.
		"/api/v1/install.sh?product=",
		// The split-download contract: magpie-agent comes from magpied,
		// otelcol-contrib comes from upstream's CDN. If anyone reverts
		// this to a one-zip-from-magpied approach the proxy timeouts come
		// back; pin it.
		"/api/v1/releases/$OS/$ARCH",
		"github.com/open-telemetry/opentelemetry-collector-releases",
		`OTELCOL_VERSION="` + install.OtelcolVersion + `"`,
		"sha256sum",
		"systemctl daemon-reload",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered bash missing %q", want)
		}
	}
}

func TestRenderPowerShellEmbedsParams(t *testing.T) {
	body, err := install.Render("powershell", install.Params{
		Server:  "https://magpie.example.com",
		Product: "observability-team",
		Variant: "windows",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(body)
	for _, want := range []string{
		"$ErrorActionPreference = 'Stop'",
		`$Server   = 'https://magpie.example.com'`,
		`$Product  = 'observability-team'`,
		`$Variant  = 'windows'`,
		"/api/v1/install.ps1?product=",
		// Split-download contract pinned for PowerShell too.
		"/api/v1/releases/$os/$arch",
		"github.com/open-telemetry/opentelemetry-collector-releases",
		"$OtelcolVersion = '" + install.OtelcolVersion + "'",
		"Get-FileHash",
		"magpie-agent.exe",
		// Service-lock fix: Stop-Service must run BEFORE Expand-Archive
		// (Windows holds an exclusive lock on running .exe files, so the
		// archive-extract overwrite fails on re-installs without this).
		// Pin the call so the order can't slip back.
		"Stop-Service -Name MagpieAgent",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered powershell missing %q", want)
		}
	}
}

// TestFromRequestRejectsShellInjection guards the input boundary: anything
// that would let `; rm -rf /` reach a `bash` pipe must die at parse time.
// Payloads are URL-encoded because that's how a real attack reaches the
// query parser — raw chars get rejected by net/url first, which is fine,
// but doesn't exercise our regex. Encoded values do.
func TestFromRequestRejectsShellInjection(t *testing.T) {
	cases := []struct {
		name string
		// rawProduct/rawVariant/rawServer are the post-decode values an
		// attacker would land in our handler. The test URL-encodes them
		// so httptest.NewRequest accepts them, but install.FromRequest
		// sees the raw bytes after net/url decodes the query.
		rawProduct, rawVariant, rawServer string
	}{
		{"semicolon", "foo;rm", "", ""},
		{"backtick", "foo`whoami`", "", ""},
		{"dollar-paren", "$(curl evil)", "", ""},
		{"newline", "foo\ncurl", "", ""},
		{"variant injection", "ok", "bar;rm", ""},
		{"javascript-server", "ok", "", "javascript:alert(1)"},
		{"file-server", "ok", "", "file:///etc/passwd"},
		{"server-with-path", "ok", "", "http://magpie.example.com/path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := url.Values{}
			if c.rawProduct != "" {
				q.Set("product", c.rawProduct)
			}
			if c.rawVariant != "" {
				q.Set("variant", c.rawVariant)
			}
			if c.rawServer != "" {
				q.Set("server", c.rawServer)
			}
			req := httptest.NewRequest("GET", "/api/v1/install.sh?"+q.Encode(), nil)
			req.Host = "magpie.example.com"
			if _, err := install.FromRequest(req); err == nil {
				t.Errorf("FromRequest accepted product=%q variant=%q server=%q; expected rejection",
					c.rawProduct, c.rawVariant, c.rawServer)
			}
		})
	}
}

// TestFromRequestDefaults: bare request with no query params should
// resolve to safe defaults without error so a curious operator hitting
// /api/v1/install.sh in a browser sees a working script, not an error.
func TestFromRequestDefaults(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/install.sh", nil)
	req.Host = "magpie.example.com:12002"
	p, err := install.FromRequest(req)
	if err != nil {
		t.Fatalf("FromRequest: %v", err)
	}
	if p.Product != "default" {
		t.Errorf("Product = %q, want default", p.Product)
	}
	if !strings.HasPrefix(p.Server, "http://magpie.example.com") {
		t.Errorf("Server = %q, want prefix http://magpie.example.com", p.Server)
	}
}
