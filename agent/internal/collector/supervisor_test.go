package collector

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// TestApplyFailsWhenBinaryMissing is the regression test for the silent-
// success bug: when the collector binary is not on disk, Apply used to
// return nil, causing the agent to report config_status=APPLIED and
// healthy=true while exporting zero telemetry. Now Apply must return
// ErrBinaryMissing so the caller surfaces FAILED / unhealthy upstream.
func TestApplyFailsWhenBinaryMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("receivers: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	missingBinary := filepath.Join(dir, "does-not-exist")
	sup := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		BinaryPath: missingBinary,
		ConfigPath: cfg,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	if sup.BinaryAvailable() {
		t.Fatalf("BinaryAvailable returned true for missing path %q", missingBinary)
	}
	if got := sup.BinaryPath(); got != missingBinary {
		t.Fatalf("BinaryPath = %q, want %q", got, missingBinary)
	}

	err := sup.Apply(context.Background())
	if err == nil {
		t.Fatal("Apply returned nil for missing binary — regression of silent-success bug")
	}
	if !errors.Is(err, ErrBinaryMissing) {
		t.Fatalf("Apply err = %v, want wrapping ErrBinaryMissing", err)
	}

	// Second call must still fail (callers must never start thinking a later
	// Apply magically succeeded just because the first warning was logged).
	if err := sup.Apply(context.Background()); !errors.Is(err, ErrBinaryMissing) {
		t.Fatalf("second Apply err = %v, want wrapping ErrBinaryMissing", err)
	}
}

// TestCollectorSurvivesCallerCtxCancel is the regression test for the
// OpAMP-disconnect-kills-collector bug. The supervisor must keep the spawned
// process alive after the caller's ctx (simulating an OpAMP message handler
// returning or a disconnect) is cancelled.
func TestCollectorSurvivesCallerCtxCancel(t *testing.T) {
	binary, args := platformSleepCommand(t)

	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("receivers: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sup := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		BinaryPath: binary,
		ConfigPath: cfg,
	})
	sup.extraArgs = args
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	if err := sup.Apply(callerCtx); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	sup.mu.Lock()
	if sup.cmd == nil || sup.cmd.Process == nil {
		sup.mu.Unlock()
		t.Fatal("expected a running child process after Apply")
	}
	pid := sup.cmd.Process.Pid
	sup.mu.Unlock()

	// Cancel the caller's ctx the way OpAMP does after OnMessage returns or
	// on websocket teardown. The collector must NOT be taken down with it.
	cancelCaller()
	time.Sleep(500 * time.Millisecond)

	if !processAlive(pid) {
		t.Fatalf("collector (pid %d) exited after caller ctx cancel — "+
			"regression of OpAMP-disconnect-kills-collector bug", pid)
	}
}

// platformSleepCommand returns a long-running command that exists on the
// host. We avoid compiling a helper to keep the test fast and hermetic.
func platformSleepCommand(t *testing.T) (string, []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		p, err := exec.LookPath("ping")
		if err != nil {
			t.Skipf("ping not found: %v", err)
		}
		return p, []string{"-n", "30", "127.0.0.1"}
	}
	p, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not found: %v", err)
	}
	return p, []string{"30"}
}

func processAlive(pid int) bool {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH", "/FO", "CSV").Output()
		if err != nil {
			return false
		}
		return len(out) > 0 && !bytes.Contains(out, []byte("No tasks"))
	}
	// kill -0 probe: returns nil if process exists and we can signal it.
	return exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil
}
