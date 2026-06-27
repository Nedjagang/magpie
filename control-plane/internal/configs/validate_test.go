package configs

import (
	"strings"
	"testing"
)

// minimalConfig is a YAML that satisfies the structural rules: one
// receiver type, one exporter type, one pipeline. Tests build off this
// by editing the receivers/processors/exporters block to inject component
// types they want to validate against an allowlist.
const minimalConfig = `
receivers:
  otlp:
    protocols:
      grpc: {}
processors:
  batch: {}
exporters:
  otlphttp:
    endpoint: https://example.com
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlphttp]
`

func TestValidate_LegacyPath_StillPasses(t *testing.T) {
	if err := Validate(minimalConfig); err != nil {
		t.Fatalf("Validate(minimalConfig) = %v, want nil", err)
	}
}

func TestValidate_RejectsForbiddenReceiver(t *testing.T) {
	// filelog is the canonical "exfiltrate from disk" receiver, and is
	// the worked example in the threat model. With an allowlist that
	// excludes it, a config using filelog must be rejected.
	cfg := strings.Replace(minimalConfig, "otlp:\n    protocols:\n      grpc: {}", "filelog:\n    include: [/etc/shadow]", 1)
	cfg = strings.Replace(cfg, "[otlp]", "[filelog]", 1)
	policy := Policy{Receivers: []string{"otlp"}, Exporters: []string{"otlphttp"}}
	err := ValidateWith(cfg, policy)
	if err == nil {
		t.Fatal("ValidateWith with filelog disallowed = nil, want error")
	}
	if !strings.Contains(err.Error(), "filelog") {
		t.Errorf("error mentions wrong component: %v", err)
	}
}

func TestValidate_AllowsListedReceiver(t *testing.T) {
	policy := Policy{Receivers: []string{"otlp"}, Exporters: []string{"otlphttp"}}
	if err := ValidateWith(minimalConfig, policy); err != nil {
		t.Fatalf("ValidateWith(minimal, allowed=otlp,otlphttp) = %v, want nil", err)
	}
}

func TestValidate_TypeBeforeSlashAllowlisted(t *testing.T) {
	// otelcol allows multiple instances via name/instance form. A config
	// using `otlp/jaeger` should pass when the allowlist contains "otlp",
	// since the suffix is operator-chosen and not a different component.
	cfg := strings.Replace(minimalConfig, "otlp:\n    protocols:\n      grpc: {}", "otlp/jaeger:\n    protocols:\n      grpc: {}", 1)
	cfg = strings.Replace(cfg, "[otlp]", "[otlp/jaeger]", 1)
	policy := Policy{Receivers: []string{"otlp"}, Exporters: []string{"otlphttp"}}
	if err := ValidateWith(cfg, policy); err != nil {
		t.Fatalf("ValidateWith(named otlp/jaeger, allowlist=[otlp]) = %v, want nil", err)
	}
}

func TestValidate_EmptyAllowlistMeansNoEnforcement(t *testing.T) {
	// Policy{} → no enforcement on any section. A "dangerous" config
	// passes — same behavior as the legacy Validate path so existing
	// callers don't break on upgrade.
	cfg := strings.Replace(minimalConfig, "otlp:\n    protocols:\n      grpc: {}", "filelog:\n    include: [/var/log/foo]", 1)
	cfg = strings.Replace(cfg, "[otlp]", "[filelog]", 1)
	if err := ValidateWith(cfg, Policy{}); err != nil {
		t.Fatalf("ValidateWith with empty Policy = %v, want nil (no enforcement)", err)
	}
}

func TestValidate_StructuralErrorWinsOverAllowlist(t *testing.T) {
	// If a config is structurally broken (no service.pipelines), the
	// operator should see the structural error first, not an allowlist
	// rejection. Otherwise small mistakes look like security findings.
	cfg := `
receivers:
  filelog: {}
exporters:
  otlphttp: {}
`
	policy := Policy{Receivers: []string{"otlp"}}
	err := ValidateWith(cfg, policy)
	if err == nil {
		t.Fatal("ValidateWith on broken config = nil, want error")
	}
	if !strings.Contains(err.Error(), "service") {
		t.Errorf("expected structural error mentioning service, got: %v", err)
	}
}
