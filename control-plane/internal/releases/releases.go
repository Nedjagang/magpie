// Package releases serves the magpie-agent binary to hosts being
// onboarded.
//
// Streamed zip contains ONLY magpie-agent[.exe] (~10 MB). The
// otelcol-contrib binary that the agent supervises is downloaded by
// the install script directly from upstream OTel — pushing ~280 MB
// through magpied (and whatever reverse proxy fronts it) was hitting
// Cloudflare body-size limits and ALB read timeouts in the wild. Per
// ADR 0008 the upstream binary ships unmodified, so going direct
// to upstream's CDN is correct, not a workaround.
//
// Directory convention (operators populate via release-bundle target):
//
//	<releases-dir>/<os>-<arch>/magpie-agent[.exe]
//
// otelcol-contrib may also live alongside magpie-agent in the same
// directory (the bundle target writes it there for offline / inspection)
// — the runtime serving path ignores it.
package releases

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Sentinel errors so the HTTP layer can map them to the right status codes
// without string-matching.
var (
	ErrInvalidPlatform      = errors.New("invalid platform identifier")
	ErrPlatformNotAvailable = errors.New("platform not available")
	ErrBinariesMissing      = errors.New("required binaries missing for platform")
	ErrAttestationMissing   = errors.New("signature or certificate not published for platform")
)

// AttestationKind names the supplementary artifacts cosign produces alongside
// a signed blob. Two kinds are served as separate endpoints so install
// scripts can fetch them with a plain HTTP GET, no multipart parsing.
type AttestationKind string

const (
	AttestationSignature   AttestationKind = "signature"
	AttestationCertificate AttestationKind = "certificate"
)

// safeIdent constrains os/arch path components so an HTTP caller can never
// request `../something` or an absolute path — only lowercase-alphanumeric
// identifiers reach the filesystem.
var safeIdent = regexp.MustCompile(`^[a-z0-9]+$`)

// Platform describes one available binary bundle.
type Platform struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	SizeBytes int64  `json:"size_bytes"`
}

// Catalog is the response body of GET /api/v1/releases.
type Catalog struct {
	Version   string     `json:"version"`
	Platforms []Platform `json:"platforms"`
}

// Store serves binaries out of a local directory. Safe for concurrent use;
// all operations are read-only and the filesystem provides the concurrency.
type Store struct {
	dir     string
	version string
}

// NewStore returns a Store backed by dir. dir does not need to exist yet —
// Catalog and WriteZip handle its absence gracefully so a fresh install
// doesn't crash before any release has been dropped in.
func NewStore(dir, version string) *Store {
	return &Store{dir: dir, version: version}
}

// Dir returns the release directory, for logging and error messages.
func (s *Store) Dir() string { return s.dir }

// Catalog walks the release directory and returns one Platform entry for
// every `<os>-<arch>/` subdirectory that has BOTH required binaries. This
// lets the UI gray out platforms that haven't been published yet.
func (s *Store) Catalog() Catalog {
	out := Catalog{Version: s.version, Platforms: []Platform{}}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		osName, arch, ok := splitPlatformDir(e.Name())
		if !ok {
			continue
		}
		agent, err := platformAgentBinary(s.dir, osName, arch)
		if err != nil {
			continue
		}
		out.Platforms = append(out.Platforms, Platform{OS: osName, Arch: arch, SizeBytes: fileSize(agent)})
	}
	sort.Slice(out.Platforms, func(i, j int) bool {
		if out.Platforms[i].OS != out.Platforms[j].OS {
			return out.Platforms[i].OS < out.Platforms[j].OS
		}
		return out.Platforms[i].Arch < out.Platforms[j].Arch
	})
	return out
}

// WriteZip streams a zip of magpie-agent for the given platform into w.
// Stored uncompressed (zip.Store) — Go binaries are already mostly
// incompressible. otelcol-contrib is intentionally NOT included; the
// install script downloads it from upstream's CDN directly so we don't
// shovel ~280 MB through whatever reverse proxy fronts magpied. On
// error, some bytes may already have been written to w; the HTTP
// handler pre-flights via os.Stat so 404s land clean before any bytes
// stream.
func (s *Store) WriteZip(w io.Writer, osName, arch string) error {
	if !safeIdent.MatchString(osName) || !safeIdent.MatchString(arch) {
		return fmt.Errorf("%w: os=%q arch=%q", ErrInvalidPlatform, osName, arch)
	}
	platformDir := filepath.Join(s.dir, osName+"-"+arch)
	if info, err := os.Stat(platformDir); err != nil || !info.IsDir() {
		return fmt.Errorf("%w: %s-%s", ErrPlatformNotAvailable, osName, arch)
	}
	agent, err := platformAgentBinary(s.dir, osName, arch)
	if err != nil {
		return err
	}

	zw := zip.NewWriter(w)
	if err := addFile(zw, agent); err != nil {
		_ = zw.Close()
		return err
	}
	return zw.Close()
}

func addFile(zw *zip.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return fmt.Errorf("zip header %s: %w", path, err)
	}
	// Flatten into a top-level file inside the zip — operators unzip it next
	// to where they'll run magpie-agent from, not into a nested directory.
	header.Name = filepath.Base(path)
	header.Method = zip.Store
	dst, err := zw.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("zip create %s: %w", path, err)
	}
	if _, err := io.Copy(dst, f); err != nil {
		return fmt.Errorf("zip copy %s: %w", path, err)
	}
	return nil
}

// WriteAttestation streams the cosign signature or certificate that goes
// with the zip for (osName, arch). Returns ErrAttestationMissing when the
// file isn't on disk — install scripts treat that as "this release was
// not signed; verification will be skipped" rather than failing.
//
// Files live alongside the agent binary using cosign's default naming:
//
//	releases/<os>-<arch>/magpie-agent[.exe].sig
//	releases/<os>-<arch>/magpie-agent[.exe].cert
//
// Tying the artifact name to the binary (not the served zip) keeps it
// stable: the zip is built per-request from the binary, so its hash
// changes every download. The binary itself is what we sign.
func (s *Store) WriteAttestation(w io.Writer, osName, arch string, kind AttestationKind) error {
	if !safeIdent.MatchString(osName) || !safeIdent.MatchString(arch) {
		return fmt.Errorf("%w: os=%q arch=%q", ErrInvalidPlatform, osName, arch)
	}
	platformDir := filepath.Join(s.dir, osName+"-"+arch)
	if info, err := os.Stat(platformDir); err != nil || !info.IsDir() {
		return fmt.Errorf("%w: %s-%s", ErrPlatformNotAvailable, osName, arch)
	}
	binary, err := platformAgentBinary(s.dir, osName, arch)
	if err != nil {
		return err
	}
	var suffix string
	switch kind {
	case AttestationSignature:
		suffix = ".sig"
	case AttestationCertificate:
		suffix = ".cert"
	default:
		return fmt.Errorf("unknown attestation kind: %q", kind)
	}
	path := binary + suffix
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrAttestationMissing, filepath.Base(path))
	}
	defer f.Close()
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("stream %s: %w", filepath.Base(path), err)
	}
	return nil
}

// platformAgentBinary resolves the magpie-agent executable path for an
// <os>-<arch> directory. Windows uses .exe; others use the
// extension-less name. Returns ErrBinariesMissing if absent so the
// HTTP handler returns a clean 404. otelcol-contrib presence in the
// same directory is irrelevant to the runtime serving path; the
// install script downloads it from upstream directly.
func platformAgentBinary(dir, osName, arch string) (string, error) {
	base := filepath.Join(dir, osName+"-"+arch)
	ext := ""
	if osName == "windows" {
		ext = ".exe"
	}
	agent := filepath.Join(base, "magpie-agent"+ext)
	if _, err := os.Stat(agent); err != nil {
		return "", fmt.Errorf("%w: missing %s", ErrBinariesMissing, filepath.Base(agent))
	}
	return agent, nil
}

func splitPlatformDir(name string) (osName, arch string, ok bool) {
	i := strings.IndexByte(name, '-')
	if i <= 0 || i == len(name)-1 {
		return "", "", false
	}
	osName, arch = name[:i], name[i+1:]
	if !safeIdent.MatchString(osName) || !safeIdent.MatchString(arch) {
		return "", "", false
	}
	return osName, arch, true
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
