// Package collector supervises the otelcol-contrib subprocess: starts it with
// the currently applied config, restarts it on config change, shuts it down
// gracefully. If the collector binary is not present on disk, Apply returns
// ErrBinaryMissing so the agent surfaces FAILED status to the control plane
// and reports itself unhealthy — never a silent no-op.
//
// Per ADR 0008, Magpie ships the upstream `otelcol-contrib` binary from
// opentelemetry-collector-releases unmodified. The default BinaryPath
// reflects that; operators who install a different OTel distribution (e.g.
// the smaller `otelcol` core binary) can point MAGPIE_COLLECTOR_BINARY at
// it — the supervisor just exec's whatever is there.
//
// Lifetime rule: the collector process runs until either a new config replaces
// it (Apply) or the agent itself is shutting down (Shutdown). It is NOT tied
// to the context of any individual caller — in particular, OpAMP disconnects
// and message-handler cancellations must not cause the collector to exit.
// This is what lets the fleet keep exporting telemetry when the control plane
// is unreachable.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// ErrBinaryMissing is returned by Apply when the otelcol-contrib executable
// cannot be found at the configured path. Callers should surface this as a
// hard failure (FAILED remote-config status, unhealthy ComponentHealth) so
// the control plane doesn't think the host is exporting telemetry when it
// isn't.
var ErrBinaryMissing = errors.New("collector binary not found")

type Supervisor struct {
	logger     *slog.Logger
	binaryPath string
	configPath string

	// rootCtx is cancelled only by Shutdown. Every spawned collector derives
	// its run-context from rootCtx, not from Apply's caller ctx.
	rootCtx    context.Context
	rootCancel context.CancelFunc

	mu            sync.Mutex
	cmd           *exec.Cmd
	cancel        context.CancelFunc
	generation    uint64 // incremented on every startLocked(); used by restartAfter to detect superseded restarts
	runWG         sync.WaitGroup
	warnedMissing bool // binary-missing warning already emitted; rate-limits the log
	stopped       bool

	// extraArgs is set only by tests to replace the "--config=..." flag with
	// args the test binary understands. Unused in production.
	extraArgs []string
}

type Options struct {
	// BinaryPath is the path to the otelcol-contrib executable. If empty, the
	// supervisor looks for it next to the magpie-agent binary.
	BinaryPath string
	// ConfigPath is the YAML file passed to the collector with --config.
	ConfigPath string
}

func New(logger *slog.Logger, opts Options) *Supervisor {
	bin := opts.BinaryPath
	if bin == "" {
		bin = defaultBinaryPath()
	}
	rootCtx, rootCancel := context.WithCancel(context.Background())
	return &Supervisor{
		logger:     logger,
		binaryPath: bin,
		configPath: opts.ConfigPath,
		rootCtx:    rootCtx,
		rootCancel: rootCancel,
	}
}

// Apply (re)starts the collector with the current config file on disk. Safe to
// call repeatedly; each call stops any running instance before spawning a new
// one. The caller's ctx governs only the start attempt — the spawned process's
// lifetime is managed by the supervisor, not the caller.
//
// Returns ErrBinaryMissing (wrapped, inspect with errors.Is) when the
// otelcol-contrib executable isn't at BinaryPath — callers must treat this as
// a hard failure rather than success.
func (s *Supervisor) Apply(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return errors.New("supervisor stopped")
	}
	if !s.binaryAvailableLocked() {
		return fmt.Errorf("%w at %s; place otelcol-contrib next to magpie-agent or set MAGPIE_COLLECTOR_BINARY", ErrBinaryMissing, s.binaryPath)
	}
	if _, err := os.Stat(s.configPath); err != nil {
		return fmt.Errorf("config file not available: %w", err)
	}

	s.stopLocked()
	return s.startLocked()
}

// BinaryAvailable reports whether the collector binary is currently on disk.
// Side-effect free — unlike the internal check used by Apply, this does not
// log or mutate state, so callers can poll it from health-reporting paths.
func (s *Supervisor) BinaryAvailable() bool {
	_, err := os.Stat(s.binaryPath)
	return err == nil
}

// BinaryPath returns the resolved path to the otelcol-contrib executable,
// useful for including in health/error messages shown to operators.
func (s *Supervisor) BinaryPath() string { return s.binaryPath }

// Shutdown terminates the collector if running and blocks until it exits.
func (s *Supervisor) Shutdown(ctx context.Context) {
	s.mu.Lock()
	s.stopped = true
	s.stopLocked()
	s.mu.Unlock()

	s.rootCancel()

	done := make(chan struct{})
	go func() { s.runWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// binaryAvailableLocked is the internal check used by Apply. Unlike the
// exported BinaryAvailable it rate-limits a warning log to once per
// supervisor lifetime so the agent stderr doesn't spam every time a new
// remote config arrives while the collector binary is still missing.
// Caller must hold s.mu.
func (s *Supervisor) binaryAvailableLocked() bool {
	if _, err := os.Stat(s.binaryPath); err != nil {
		if !s.warnedMissing {
			s.logger.Error("collector binary not found; agent will report FAILED",
				"path", s.binaryPath,
				"hint", "place otelcol-contrib next to magpie-agent or set MAGPIE_COLLECTOR_BINARY")
			s.warnedMissing = true
		}
		return false
	}
	return true
}

func (s *Supervisor) startLocked() error {
	// Derive the collector's run context from rootCtx — NOT from any caller's
	// ctx. This is the load-bearing line for fleet resilience: it means the
	// collector survives OpAMP disconnects, handler returns, and any other
	// caller-scoped cancellation.
	runCtx, cancel := context.WithCancel(s.rootCtx)
	args := s.extraArgs
	if args == nil {
		args = []string{"--config=" + s.configPath}
	}
	cmd := exec.CommandContext(runCtx, s.binaryPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start collector: %w", err)
	}
	s.generation++
	gen := s.generation
	s.logger.Info("collector started", "pid", cmd.Process.Pid, "config", s.configPath)
	s.cmd = cmd
	s.cancel = cancel

	s.runWG.Add(1)
	go func() {
		defer s.runWG.Done()
		err := cmd.Wait()
		if err != nil && runCtx.Err() == nil {
			s.logger.Error("collector exited unexpectedly", "err", err)
			go s.restartAfter(5*time.Second, gen)
		} else {
			s.logger.Info("collector exited")
		}
	}()
	return nil
}

// restartAfter waits d, then restarts the collector — but only if the
// supervisor hasn't been stopped and no newer Apply() has already taken over
// (detected via the generation counter). This ensures unexpected collector
// crashes self-heal without the operator needing to push a new config or
// restart the agent service.
func (s *Supervisor) restartAfter(d time.Duration, gen uint64) {
	select {
	case <-s.rootCtx.Done():
		return
	case <-time.After(d):
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.generation != gen {
		// Supervisor is shutting down, or Apply() already launched a new
		// collector — either way, this restart is superseded.
		return
	}
	s.logger.Info("restarting collector after unexpected exit")
	s.stopLocked() // clean up the zombie cmd/cancel before starting fresh
	if err := s.startLocked(); err != nil {
		s.logger.Error("collector restart failed", "err", err)
	}
}

func (s *Supervisor) stopLocked() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	s.cancel()
	// Give the collector up to 5s to exit via context cancel; then force kill.
	done := make(chan struct{})
	go func() { _ = s.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
	}
	s.cmd = nil
	s.cancel = nil
}

func defaultBinaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "otelcol-contrib"
	}
	name := "otelcol-contrib"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(filepath.Dir(exe), name)
}
