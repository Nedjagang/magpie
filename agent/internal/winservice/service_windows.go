//go:build windows

package winservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// SetMachineEnv writes a machine-scope environment variable to the registry.
//
// WARNING: this does NOT make the variable visible to already-running services.
// The Service Control Manager caches its environment at boot — you'd need a
// reboot for new machine-scope vars to propagate to services. For service
// configuration, use WriteParams (which stores config under the service's
// own registry key and is read fresh on every service start).
func SetMachineEnv(key, value string) error {
	k, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Session Manager\Environment`,
		registry.SET_VALUE,
	)
	if err != nil {
		return fmt.Errorf("open env registry key: %w", err)
	}
	defer k.Close()
	if err := k.SetStringValue(key, value); err != nil {
		return fmt.Errorf("set %s: %w", key, err)
	}
	return nil
}

// paramsKeyPath is the service-specific registry key where we store agent
// config. SCM creates the parent Services\MagpieAgent key when the service
// is installed; we create Parameters underneath it.
//
// Why this rather than env vars: the SCM process caches the machine
// environment at boot, so Machine-scope env var changes don't reach
// services without a reboot. Registry reads are fresh on every service
// start, so changes take effect immediately on restart.
const paramsKeyPath = `SYSTEM\CurrentControlSet\Services\` + ServiceName + `\Parameters`

// WriteParams persists agent configuration under the service's Parameters
// registry key. Overwrites existing values. The agent reads these at
// startup and merges them into process environment.
func WriteParams(params map[string]string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, paramsKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("create params key: %w", err)
	}
	defer k.Close()
	for name, value := range params {
		if err := k.SetStringValue(name, value); err != nil {
			return fmt.Errorf("set %s: %w", name, err)
		}
	}
	return nil
}

// ReadParams returns every string value under the Parameters key. Returns
// (nil, nil) when the key doesn't exist yet — expected for fresh foreground
// runs before install has been run.
func ReadParams() (map[string]string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, paramsKeyPath, registry.QUERY_VALUE)
	if err != nil {
		// Swallow "key not found" — it's the normal case for foreground use.
		return nil, nil //nolint:nilerr
	}
	defer k.Close()

	names, err := k.ReadValueNames(-1)
	if err != nil {
		return nil, fmt.Errorf("list param names: %w", err)
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		v, _, err := k.GetStringValue(n)
		if err != nil {
			continue
		}
		out[n] = v
	}
	return out, nil
}

// DeleteParams removes the Parameters key. Called from Uninstall so the
// agent's config doesn't linger after removal.
func DeleteParams() error {
	err := registry.DeleteKey(registry.LOCAL_MACHINE, paramsKeyPath)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// ServiceName is the identifier used by Windows SCM. Kept short and
// CamelCase so it fits nicely in `sc.exe query` output.
const ServiceName = "MagpieAgent"

// ServiceDescription shows up in services.msc.
const ServiceDescription = "Magpie telemetry agent — manages the local otelcol-contrib subprocess and pulls configuration from the Magpie control plane."

// IsServiceMode returns true when the current process was started by the
// Windows SCM rather than an interactive user.
func IsServiceMode() bool {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	return isService
}

// Run wraps the given RunFunc in a Windows Service handler. Blocks until
// SCM sends Stop or the RunFunc returns.
func Run(logger *slog.Logger, rf RunFunc) error {
	return svc.Run(ServiceName, &handler{logger: logger, rf: rf})
}

type handler struct {
	logger *slog.Logger
	rf     RunFunc
}

func (h *handler) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (ssec bool, errno uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	s <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- h.rf(ctx, h.logger) }()

	s <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case err := <-runErr:
			if err != nil {
				h.logger.Error("agent loop exited", "err", err)
				s <- svc.Status{State: svc.Stopped}
				return false, 1
			}
			s <- svc.Status{State: svc.Stopped}
			return false, 0
		case req := <-r:
			switch req.Cmd {
			case svc.Interrogate:
				s <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				cancel()
				// Wait briefly for the agent loop to tear down; 30s upper
				// bound is well within Windows' default service stop timeout.
				select {
				case <-runErr:
				case <-time.After(30 * time.Second):
					h.logger.Warn("agent did not exit within 30s of stop; forcing")
				}
				s <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		}
	}
}

// Install registers the service with SCM and starts it immediately. Idempotent.
func Install(logger *slog.Logger) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return fmt.Errorf("absolute path: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM (run as Administrator?): %w", err)
	}
	defer m.Disconnect()

	// If the service already exists, update the binary path in case the
	// user moved the install directory, then (re)start.
	if existing, err := m.OpenService(ServiceName); err == nil {
		defer existing.Close()
		logger.Info("service already exists, updating config and restarting")
		cfg, err := existing.Config()
		if err != nil {
			return fmt.Errorf("read existing config: %w", err)
		}
		cfg.BinaryPathName = exe
		cfg.StartType = mgr.StartAutomatic
		cfg.DisplayName = ServiceName
		cfg.Description = ServiceDescription
		if err := existing.UpdateConfig(cfg); err != nil {
			return fmt.Errorf("update config: %w", err)
		}
		if err := setRecoveryActions(existing); err != nil {
			logger.Warn("could not set service recovery actions; service won't auto-restart on crash", "err", err)
		}
		_, _ = existing.Control(svc.Stop)
		// Wait for stop to complete, then start fresh.
		waitUntilStopped(existing)
		if err := existing.Start(); err != nil {
			return fmt.Errorf("start service: %w", err)
		}
		fmt.Println("✓ MagpieAgent service reconfigured and started")
		return nil
	}

	s, err := m.CreateService(ServiceName, exe, mgr.Config{
		DisplayName: ServiceName,
		Description: ServiceDescription,
		StartType:   mgr.StartAutomatic,
	})
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	if err := setRecoveryActions(s); err != nil {
		logger.Warn("could not set service recovery actions; service won't auto-restart on crash", "err", err)
	}

	// Register an event log source so service messages show up nicely in
	// Event Viewer. Non-fatal if it fails (e.g. source already exists).
	_ = eventlog.InstallAsEventCreate(ServiceName, eventlog.Info|eventlog.Warning|eventlog.Error)

	if err := s.Start(); err != nil {
		// Roll back the service registration if Start fails, otherwise the
		// user is left with a misconfigured service.
		_ = s.Delete()
		return fmt.Errorf("start service: %w", err)
	}
	fmt.Println("✓ MagpieAgent service installed and started")
	fmt.Println("  verify: sc.exe query MagpieAgent")
	return nil
}

// Uninstall stops and removes the service. Idempotent.
func Uninstall(_ *slog.Logger) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM (run as Administrator?): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		fmt.Println("MagpieAgent service is not installed — nothing to do.")
		return nil
	}
	defer s.Close()

	_, _ = s.Control(svc.Stop)
	waitUntilStopped(s)

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	_ = eventlog.Remove(ServiceName)
	_ = DeleteParams()
	fmt.Println("✓ MagpieAgent service stopped and removed")
	return nil
}

// Status prints the current service state.
func Status(_ *slog.Logger) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		fmt.Println("MagpieAgent: not installed")
		return nil
	}
	defer s.Close()

	st, err := s.Query()
	if err != nil {
		return fmt.Errorf("query status: %w", err)
	}
	fmt.Printf("MagpieAgent: %s (PID %d)\n", stateString(st.State), st.ProcessId)
	return nil
}

// setRecoveryActions configures the service to auto-restart on crash. SCM's
// default is "do nothing" on service failure — without this, an agent that
// the watchdog forces to exit (because its OpAMP WebSocket went stale) stays
// dead until manually restarted. We pin three Restart actions with a 60-second
// delay and a 24h failure-counter reset so transient ones don't escalate to
// a real outage but a stuck/crashing build won't loop fast either.
func setRecoveryActions(s *mgr.Service) error {
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	const resetPeriodSec = 24 * 60 * 60
	return s.SetRecoveryActions(actions, resetPeriodSec)
}

func waitUntilStopped(s *mgr.Service) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil || st.State == svc.Stopped {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func stateString(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue-pending"
	case svc.PausePending:
		return "pause-pending"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("unknown (%d)", s)
	}
}
