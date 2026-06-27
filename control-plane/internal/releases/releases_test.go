package releases

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// seedPlatform creates a fake <os>-<arch>/ directory with both required
// binaries, matching the layout the CI release workflow produces.
func seedPlatform(t *testing.T, root, osName, arch string, agentBody, otelcolBody []byte) {
	t.Helper()
	dir := filepath.Join(root, osName+"-"+arch)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ext := ""
	if osName == "windows" {
		ext = ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, "magpie-agent"+ext), agentBody, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "otelcol-contrib"+ext), otelcolBody, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogListsSeededPlatformsOnly(t *testing.T) {
	root := t.TempDir()
	seedPlatform(t, root, "windows", "amd64", []byte("agent-win"), []byte("col-win"))
	seedPlatform(t, root, "linux", "amd64", []byte("agent-lin"), []byte("col-lin"))
	// magpie-agent-less platform: only otelcol-contrib, no magpie-agent.
	// Must be excluded — magpie-agent is the only thing the runtime
	// endpoint serves, so a directory without it is not a usable
	// platform.
	if err := os.MkdirAll(filepath.Join(root, "darwin-arm64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "darwin-arm64", "otelcol-contrib"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// And a bogus non-platform directory.
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := NewStore(root, "0.1.0").Catalog()

	if got.Version != "0.1.0" {
		t.Errorf("Version = %q, want 0.1.0", got.Version)
	}

	names := []string{}
	for _, p := range got.Platforms {
		names = append(names, p.OS+"-"+p.Arch)
	}
	sort.Strings(names)
	want := []string{"linux-amd64", "windows-amd64"}
	if len(names) != len(want) {
		t.Fatalf("Platforms = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("Platforms[%d] = %q, want %q", i, names[i], want[i])
		}
	}

	// SizeBytes is the magpie-agent binary size (otelcol-contrib not in
	// the served zip, so not in the reported size either).
	for _, p := range got.Platforms {
		if p.SizeBytes <= 0 {
			t.Errorf("%s-%s: SizeBytes = %d, want > 0", p.OS, p.Arch, p.SizeBytes)
		}
	}
}

func TestCatalogReturnsEmptyOnMissingDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-there-yet")
	got := NewStore(root, "v1").Catalog()
	if len(got.Platforms) != 0 {
		t.Errorf("Platforms = %v, want empty", got.Platforms)
	}
	if got.Version != "v1" {
		t.Errorf("Version = %q, want v1", got.Version)
	}
}

func TestWriteZipProducesAgentOnly(t *testing.T) {
	// otelcol-contrib is intentionally excluded from the served zip — the
	// install script downloads it from upstream's CDN to avoid pushing
	// ~280 MB through the magpied reverse proxy. Test pins that contract:
	// even when both binaries exist on disk, the zip carries only one.
	root := t.TempDir()
	seedPlatform(t, root, "linux", "amd64", []byte("AGENT-BODY"), []byte("COL-BODY"))

	var buf bytes.Buffer
	if err := NewStore(root, "v1").WriteZip(&buf, "linux", "amd64"); err != nil {
		t.Fatalf("WriteZip: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	contents := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		contents[f.Name] = string(body)
	}

	if got, want := contents["magpie-agent"], "AGENT-BODY"; got != want {
		t.Errorf("magpie-agent body = %q, want %q", got, want)
	}
	if _, present := contents["otelcol-contrib"]; present {
		t.Errorf("zip contains otelcol-contrib; install script downloads it from upstream CDN now")
	}
	if len(contents) != 1 {
		t.Errorf("zip files = %v, want exactly 1 entry (magpie-agent only)", contents)
	}
}

func TestWriteZipWindowsAgentHasDotExe(t *testing.T) {
	root := t.TempDir()
	seedPlatform(t, root, "windows", "amd64", []byte("A"), []byte("B"))

	var buf bytes.Buffer
	if err := NewStore(root, "v1").WriteZip(&buf, "windows", "amd64"); err != nil {
		t.Fatalf("WriteZip: %v", err)
	}
	zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	want := []string{"magpie-agent.exe"}
	if len(names) != 1 || names[0] != want[0] {
		t.Fatalf("zip entries = %v, want %v (otelcol-contrib excluded by design)", names, want)
	}
}

func TestWriteZipRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root, "v1")

	// Classic traversal attempts and anything not matching [a-z0-9]+ must
	// never reach os.Stat — they short-circuit as ErrInvalidPlatform.
	cases := []struct{ os, arch string }{
		{"../etc", "amd64"},
		{"linux", "../../../bin"},
		{"Linux", "amd64"},        // uppercase rejected
		{"linux amd64", ""},       // space rejected
		{"linux", "amd64;rm -rf"}, // shell-meta rejected
	}
	for _, c := range cases {
		err := s.WriteZip(io.Discard, c.os, c.arch)
		if !errors.Is(err, ErrInvalidPlatform) {
			t.Errorf("WriteZip(%q,%q) err = %v, want ErrInvalidPlatform", c.os, c.arch, err)
		}
	}
}

// seedAttestation drops a cosign sidecar (.sig or .cert) next to the
// magpie-agent binary, mirroring what the release workflow does after
// `cosign sign-blob`. Centralised here so the cases below stay legible.
func seedAttestation(t *testing.T, root, osName, arch, suffix string, body []byte) {
	t.Helper()
	ext := ""
	if osName == "windows" {
		ext = ".exe"
	}
	path := filepath.Join(root, osName+"-"+arch, "magpie-agent"+ext+suffix)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWriteAttestationStreamsSignatureWhenPresent(t *testing.T) {
	root := t.TempDir()
	seedPlatform(t, root, "linux", "amd64", []byte("a"), []byte("c"))
	seedAttestation(t, root, "linux", "amd64", ".sig", []byte("SIG-BYTES"))
	seedAttestation(t, root, "linux", "amd64", ".cert", []byte("CERT-BYTES"))

	s := NewStore(root, "v1")

	var sig, cert bytes.Buffer
	if err := s.WriteAttestation(&sig, "linux", "amd64", AttestationSignature); err != nil {
		t.Fatalf("WriteAttestation(sig): %v", err)
	}
	if got := sig.String(); got != "SIG-BYTES" {
		t.Errorf("signature body = %q, want SIG-BYTES", got)
	}
	if err := s.WriteAttestation(&cert, "linux", "amd64", AttestationCertificate); err != nil {
		t.Fatalf("WriteAttestation(cert): %v", err)
	}
	if got := cert.String(); got != "CERT-BYTES" {
		t.Errorf("certificate body = %q, want CERT-BYTES", got)
	}
}

func TestWriteAttestationMissingReturnsSentinel(t *testing.T) {
	// Binary present but no .sig/.cert next to it. This is the v0.2
	// no-CI-signing-yet path — handler must return ErrAttestationMissing
	// so the HTTP layer maps to 404 and the install script can decide
	// whether to fail-closed or warn-and-continue.
	root := t.TempDir()
	seedPlatform(t, root, "linux", "amd64", []byte("a"), []byte("c"))

	s := NewStore(root, "v1")
	err := s.WriteAttestation(io.Discard, "linux", "amd64", AttestationSignature)
	if !errors.Is(err, ErrAttestationMissing) {
		t.Errorf("WriteAttestation no .sig: err = %v, want ErrAttestationMissing", err)
	}
}

func TestWriteAttestationRejectsPathTraversalAndUnknownKind(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root, "v1")

	if err := s.WriteAttestation(io.Discard, "../etc", "amd64", AttestationSignature); !errors.Is(err, ErrInvalidPlatform) {
		t.Errorf("traversal: err = %v, want ErrInvalidPlatform", err)
	}
	if err := s.WriteAttestation(io.Discard, "linux", "amd64", AttestationKind("provenance")); err == nil {
		t.Errorf("unknown kind: err = nil, want non-nil")
	}
}

func TestWriteZipNotAvailableAndMissingAgent(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root, "v1")

	// Platform dir doesn't exist at all.
	if err := s.WriteZip(io.Discard, "linux", "amd64"); !errors.Is(err, ErrPlatformNotAvailable) {
		t.Errorf("missing dir: err = %v, want ErrPlatformNotAvailable", err)
	}

	// Platform dir exists with only otelcol-contrib (no magpie-agent).
	// Should fail because magpie-agent is the only binary the runtime
	// endpoint cares about.
	dir := filepath.Join(root, "linux-amd64")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "otelcol-contrib"), []byte("c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteZip(io.Discard, "linux", "amd64"); !errors.Is(err, ErrBinariesMissing) {
		t.Errorf("agent-missing: err = %v, want ErrBinariesMissing", err)
	}

	// Inverse: magpie-agent alone is sufficient now.
	if err := os.WriteFile(filepath.Join(dir, "magpie-agent"), []byte("a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "otelcol-contrib")); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteZip(io.Discard, "linux", "amd64"); err != nil {
		t.Errorf("agent-only: err = %v, want nil (otelcol-contrib not required at runtime)", err)
	}
}
