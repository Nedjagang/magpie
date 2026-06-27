package configs

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Policy controls semantic validation in addition to the structural rules
// Validate always applies. A zero-value Policy (all allowlists nil) means
// "no component allowlisting" — every otelcol component type passes,
// matching v0.1 behavior. Set the slices to opt into deny-by-default
// allowlisting per section.
//
// Zero-value cleanly represents "feature off" so default-constructed Policies
// behave like legacy callers; only operators who set MAGPIE_ALLOWED_*
// pull the gate up.
type Policy struct {
	Receivers  []string
	Processors []string
	Exporters  []string
	Extensions []string
	Connectors []string
}

// Validate performs a structural sanity check on a collector config YAML
// against the default (no-allowlist) Policy. Equivalent to ValidateWith
// (Policy{}).
//
// It is intentionally permissive — the goal is to reject obvious authoring
// mistakes before they hit the fleet, not to substitute for the collector's
// own validation at apply time.
//
// Rules:
//   - must parse as YAML
//   - top level must be a mapping
//   - must contain 'service.pipelines'
//   - every pipeline must reference at least one receiver and one exporter
//   - every receiver/exporter name referenced in a pipeline must exist in
//     the top-level receivers/exporters section
func Validate(y string) error {
	return ValidateWith(y, Policy{})
}

// ValidateWith runs structural rules then enforces a Policy:
// every component type used by the YAML must be in the matching allowlist
// (when the allowlist is non-empty). Component "type" is the prefix before
// the first slash — e.g. "filelog/web" → "filelog" — matching otelcol's
// own naming convention.
//
// Empty allowlist on a section = "no enforcement on that section."
// Operators wanting deny-by-default set every section explicitly.
func ValidateWith(y string, p Policy) error {
	if len(y) == 0 {
		return errors.New("config is empty")
	}
	y = normalizeNullMapKeys(y)
	y = stripBlankLines(y)
	var root map[string]any
	if err := yaml.Unmarshal([]byte(y), &root); err != nil {
		return fmt.Errorf("invalid yaml: %w", err)
	}
	if root == nil {
		return errors.New("config root must be a mapping")
	}

	receivers := keysOf(root["receivers"])
	exporters := keysOf(root["exporters"])

	svc, _ := root["service"].(map[string]any)
	if svc == nil {
		return errors.New("missing 'service' section")
	}
	pipelines, _ := svc["pipelines"].(map[string]any)
	if len(pipelines) == 0 {
		return errors.New("missing 'service.pipelines'")
	}

	for name, raw := range pipelines {
		pl, _ := raw.(map[string]any)
		if pl == nil {
			return fmt.Errorf("pipeline %q must be a mapping", name)
		}
		pr := stringList(pl["receivers"])
		pe := stringList(pl["exporters"])
		if len(pr) == 0 {
			return fmt.Errorf("pipeline %q has no receivers", name)
		}
		if len(pe) == 0 {
			return fmt.Errorf("pipeline %q has no exporters", name)
		}
		for _, r := range pr {
			if _, ok := receivers[r]; !ok {
				return fmt.Errorf("pipeline %q references undefined receiver %q", name, r)
			}
		}
		for _, e := range pe {
			if _, ok := exporters[e]; !ok {
				return fmt.Errorf("pipeline %q references undefined exporter %q", name, e)
			}
		}
	}

	// Section-by-section allowlist enforcement. Done after structural
	// checks so an operator gets the most informative error first
	// ("you have no exporters" beats "filelog isn't allowed").
	for _, check := range []struct {
		section string
		allowed []string
	}{
		{"receivers", p.Receivers},
		{"processors", p.Processors},
		{"exporters", p.Exporters},
		{"extensions", p.Extensions},
		{"connectors", p.Connectors},
	} {
		if len(check.allowed) == 0 {
			continue
		}
		if err := enforceAllowlist(check.section, root[check.section], check.allowed); err != nil {
			return err
		}
	}
	return nil
}

// enforceAllowlist rejects any component in `node` whose type is not in
// `allowed`. Component "type" is the key with anything from "/" onward
// stripped — otelcol uses `name/instance` to allow multiple instances of
// the same component (e.g. `otlp/jaeger`, `otlp/datadog`). The instance
// suffix is operator-chosen; the type before it is what the allowlist is
// gating on.
func enforceAllowlist(section string, node any, allowed []string) error {
	m, ok := node.(map[string]any)
	if !ok {
		// Either the section is absent (nil) or shaped wrong — both are
		// fine for the allowlist's purposes (nothing to gate).
		return nil
	}
	allowSet := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		allowSet[strings.TrimSpace(a)] = struct{}{}
	}
	for key := range m {
		typ, _, _ := strings.Cut(key, "/")
		if _, ok := allowSet[typ]; !ok {
			return fmt.Errorf("%s component %q is not in MAGPIE_ALLOWED_%s allowlist", section, key, strings.ToUpper(section))
		}
	}
	return nil
}

// normalizeNullMapKeys rewrites bare map keys (e.g. `cpu:` with no value)
// to explicit empty maps (`cpu: {}`). yaml.v3 can fail to parse valid OTel
// configs where null-value keys appear before a blank line or a less-indented
// block — this pre-pass makes the structure unambiguous without changing semantics.
// Keys that already have an inline value or have child lines are left untouched.
func normalizeNullMapKeys(y string) string {
	lines := strings.Split(y, "\n")
	for i, line := range lines {
		stripped := strings.TrimLeft(line, " \t")
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}
		colonIdx := strings.Index(stripped, ":")
		if colonIdx < 0 {
			continue
		}
		after := strings.TrimSpace(stripped[colonIdx+1:])
		// Skip lines that already have an inline value (not a comment).
		if after != "" && !strings.HasPrefix(after, "#") {
			continue
		}
		currentIndent := len(line) - len(stripped)
		// If any following non-blank, non-comment line is more indented,
		// this key has children — leave it alone.
		hasChildren := false
		for _, nl := range lines[i+1:] {
			ns := strings.TrimLeft(nl, " \t")
			if ns == "" || strings.HasPrefix(ns, "#") {
				continue
			}
			hasChildren = len(nl)-len(ns) > currentIndent
			break
		}
		if hasChildren {
			continue
		}
		indent := line[:len(line)-len(stripped)]
		key := stripped[:colonIdx]
		comment := ""
		if strings.HasPrefix(after, "#") {
			comment = " " + after
		}
		lines[i] = indent + key + ": {}" + comment
	}
	return strings.Join(lines, "\n")
}

// stripBlankLines removes blank (whitespace-only) lines before parsing.
// yaml.v3 v3.0.1 misparses valid OTel configs when blank lines appear between
// a deeply-nested block entry and its less-indented parent key — it emits
// "did not find expected key". Blank lines are cosmetic in YAML block mappings
// and carry no semantic meaning for OTel configs.
func stripBlankLines(y string) string {
	lines := strings.Split(y, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func keysOf(v any) map[string]struct{} {
	out := map[string]struct{}{}
	m, _ := v.(map[string]any)
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

func stringList(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
