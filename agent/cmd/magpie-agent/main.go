// Command magpie-agent is the Magpie agent supervisor.
//
// Subcommands (Windows only):
//
//	magpie-agent install    — register and start MagpieAgent Windows Service
//	magpie-agent uninstall  — stop and remove the service
//	magpie-agent run        — run in foreground (default when no subcommand)
//
// When launched by the Windows Service Control Manager the binary detects
// service context automatically and integrates with SCM — no subcommand is
// required there.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/open-telemetry/opamp-go/client"
	clienttypes "github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/magpie-project/magpie/agent/internal/collector"
	"github.com/magpie-project/magpie/agent/internal/machineid"
	"github.com/magpie-project/magpie/agent/internal/winservice"
)

// effectiveCfg holds the last config body persisted to disk; it is the
// authoritative "effective config" reported back to the server.
type effectiveCfg struct {
	mu   sync.RWMutex
	body []byte
	hash []byte
}

func (e *effectiveCfg) set(body, hash []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.body = body
	e.hash = hash
}

func (e *effectiveCfg) get() ([]byte, []byte) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.body, e.hash
}

var version = "0.2.0-dev"

// lastConnectUnix is set by the OpAMP OnConnect callback to time.Now().Unix()
// each time the WebSocket establishes (initial connect + every reconnect).
// The reconnect watchdog reads this to decide whether opamp-go's internal
// retry has stalled and we need to force-exit so SCM restarts the process.
var lastConnectUnix atomic.Int64

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Load any agent config stored in the service's registry Parameters key.
	// This is the primary config channel for the Windows Service path because
	// the SCM caches its environment at boot — machine-scope env vars set
	// AFTER boot won't reach services without a reboot. Registry reads are
	// fresh on every service start, so changes take effect on `sc stop/start`
	// with no reboot needed.
	//
	// Existing env vars take precedence so interactive foreground runs can
	// still override via $env:.
	if params, err := winservice.ReadParams(); err == nil {
		for k, v := range params {
			if os.Getenv(k) == "" {
				_ = os.Setenv(k, v)
			}
		}
	}

	// When launched by the Windows SCM, hand control to the service runner —
	// it will call run(ctx) under SCM's control and respond to Stop events.
	if winservice.IsServiceMode() {
		if err := winservice.Run(logger, run); err != nil {
			logger.Error("service run failed", "err", err)
			os.Exit(1)
		}
		return
	}

	// Subcommand dispatch for interactive use.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install":
			if err := installCommand(logger, os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "install failed:", err)
				os.Exit(1)
			}
			return
		case "uninstall":
			if err := winservice.Uninstall(logger); err != nil {
				fmt.Fprintln(os.Stderr, "uninstall failed:", err)
				os.Exit(1)
			}
			return
		case "status":
			if err := winservice.Status(logger); err != nil {
				fmt.Fprintln(os.Stderr, "status failed:", err)
				os.Exit(1)
			}
			return
		case "run":
			// fall through to foreground run
		case "-h", "--help", "help":
			printUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
			printUsage()
			os.Exit(2)
		}
	}

	// Default: run in foreground with signal-driven shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("agent exited with error", "err", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `magpie-agent %s

Usage:
  magpie-agent                 run in foreground (Ctrl+C to stop)
  magpie-agent run             same as above
  magpie-agent install [flags] install and start as Windows Service (admin required)
  magpie-agent uninstall       stop and remove the Windows Service
  magpie-agent status          show service status
  magpie-agent help            print this help

Install flags (all but -token are required the first time; saved at
Machine environment scope so the service sees them on boot):
  -server   <url-or-host>     e.g. 34.203.177.194  or  ws://host:12002/v1/opamp
  -product  <name>            product cohort, e.g. "observability"
  -variant  <name>            variant cohort, e.g. "windows"
  -name     <display-name>    optional; defaults to $env:COMPUTERNAME
  -token    <bearer-token>    magpied API token (matches MAGPIE_API_TOKEN on
                              the control plane). Required when targeting a
                              v0.2+ control plane; optional for v0.1.

Example:
  magpie-agent install -server 34.203.177.194 -product observability -variant windows -token <token>

Environment (read at runtime, Machine scope required for services):
  MAGPIE_SERVER_URL   ws://<magpied>:12002/v1/opamp
  MAGPIE_PRODUCT      product cohort
  MAGPIE_VARIANT      variant cohort
  MAGPIE_AGENT_NAME   display name in UI
  MAGPIE_API_TOKEN    bearer token (v0.2+; omit for v0.1 control planes)
`, version)
}

// installCommand parses the install subcommand's flags, persists them as
// Machine-scope environment variables (so Windows Services see them), then
// delegates to winservice.Install. The flag-driven path exists because the
// older "set $env: vars, then run install" flow silently installed the
// service with the wrong config — $env: vars are only visible to the
// current shell, not the LocalSystem service that Windows spawns.
func installCommand(logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	var (
		server  = fs.String("server", "", "control-plane host or full ws:// URL (e.g. 34.203.177.194 or ws://host:12002/v1/opamp)")
		product = fs.String("product", "", "product cohort to join (e.g. observability)")
		variant = fs.String("variant", "", "variant cohort (e.g. windows / linux / kubernetes)")
		name    = fs.String("name", "", "display name in the UI (default: hostname)")
		token   = fs.String("token", "", "magpied API bearer token (matches the server's MAGPIE_API_TOKEN). Optional only when targeting a v0.1 control plane.")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Pull from Machine-scope env as a fallback so operators who already ran
	// SetEnvironmentVariable(...,"Machine") can re-run `install` without
	// re-specifying the values.
	resolve := func(flagVal, envKey string) string {
		if flagVal != "" {
			return flagVal
		}
		return os.Getenv(envKey)
	}
	resolved := struct {
		server, product, variant, name, token string
	}{
		server:  resolve(*server, "MAGPIE_SERVER_URL"),
		product: resolve(*product, "MAGPIE_PRODUCT"),
		variant: resolve(*variant, "MAGPIE_VARIANT"),
		name:    resolve(*name, "MAGPIE_AGENT_NAME"),
		token:   resolve(*token, "MAGPIE_API_TOKEN"),
	}

	missing := []string{}
	if resolved.server == "" {
		missing = append(missing, "-server")
	}
	if resolved.product == "" {
		missing = append(missing, "-product")
	}
	if resolved.variant == "" {
		missing = append(missing, "-variant")
	}
	if len(missing) > 0 {
		return fmt.Errorf("required flags missing: %v — run `magpie-agent help` for usage", missing)
	}

	if resolved.name == "" {
		if host, err := os.Hostname(); err == nil {
			resolved.name = host
		}
	}

	// Normalize -server. The PowerShell installer passes the magpied URL
	// it was loaded from (e.g. "https://magpie.example.com"), and humans
	// pass anything from a bare IP to a full ws:// URL — we accept the
	// lot and produce a single canonical OpAMP WebSocket URL.
	serverURL, err := normalizeServerURL(resolved.server)
	if err != nil {
		return fmt.Errorf("invalid -server: %w", err)
	}

	// Persist under the service's Parameters registry key. The agent reads
	// this on startup (including service-start), so values take effect on
	// `install` without needing a reboot — unlike Machine-scope env vars,
	// which are cached by SCM at boot and don't propagate to services
	// without rebooting the host.
	params := map[string]string{
		"MAGPIE_SERVER_URL": serverURL,
		"MAGPIE_PRODUCT":    resolved.product,
		"MAGPIE_VARIANT":    resolved.variant,
		"MAGPIE_AGENT_NAME": resolved.name,
	}
	// Only persist the token when supplied. Mirror choice for the env-var
	// fallback below: an unset token leaves the service in v0.1 no-auth
	// mode, matching the magpied-side default and not silently destroying
	// any token a previous install wrote.
	if resolved.token != "" {
		params["MAGPIE_API_TOKEN"] = resolved.token
	}
	if err := winservice.WriteParams(params); err != nil {
		return fmt.Errorf("write service params to registry: %w", err)
	}

	// Also mirror to Machine-scope env so interactive (non-service) runs
	// from a fresh shell pick them up. Best-effort — not fatal if it fails.
	for k, v := range params {
		_ = winservice.SetMachineEnv(k, v)
	}

	fmt.Printf("→ configured in HKLM\\SYSTEM\\CurrentControlSet\\Services\\MagpieAgent\\Parameters:\n")
	fmt.Printf("    MAGPIE_SERVER_URL = %s\n", serverURL)
	fmt.Printf("    MAGPIE_PRODUCT    = %s\n", resolved.product)
	fmt.Printf("    MAGPIE_VARIANT    = %s\n", resolved.variant)
	fmt.Printf("    MAGPIE_AGENT_NAME = %s\n", resolved.name)
	if resolved.token != "" {
		fmt.Printf("    MAGPIE_API_TOKEN  = (set, sha256:%s…)\n", fmt.Sprintf("%x", sha256.Sum256([]byte(resolved.token)))[:12])
	} else {
		fmt.Printf("    MAGPIE_API_TOKEN  = (not set; agent will connect without auth)\n")
	}
	fmt.Println()

	return winservice.Install(logger)
}

// run is the agent's main loop. It connects to the control plane, applies
// incoming configs, supervises the collector subprocess, and returns only
// when ctx is cancelled or a fatal error occurs. Factored out of main() so
// the Windows Service handler can invoke it with an SCM-controlled ctx.
func run(ctx context.Context, logger *slog.Logger) error {
	serverURL := envOr("MAGPIE_SERVER_URL", "ws://localhost:12002/v1/opamp")
	logger.Info("magpie agent starting", "version", version, "server", serverURL)

	configPath := envOr("MAGPIE_AGENT_CONFIG_PATH", "magpie-agent-config.yaml")
	absConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		absConfigPath = configPath
	}

	supervisor := collector.New(logger, collector.Options{
		BinaryPath: os.Getenv("MAGPIE_COLLECTOR_BINARY"),
		ConfigPath: absConfigPath,
	})

	startNanos := uint64(time.Now().UnixNano())

	// buildHealth translates supervisor state into a ComponentHealth. Called
	// at startup and after each Apply so the UI reflects reality: Healthy
	// must be false whenever the collector isn't actually running, otherwise
	// operators stare at "green" agents that export zero telemetry.
	buildHealth := func(supErr error) *protobufs.ComponentHealth {
		h := &protobufs.ComponentHealth{StartTimeUnixNano: startNanos}
		switch {
		case supErr != nil && errors.Is(supErr, collector.ErrBinaryMissing):
			h.Healthy = false
			h.Status = "collector binary missing"
			h.LastError = supErr.Error()
		case supErr != nil:
			h.Healthy = false
			h.Status = "collector apply failed"
			h.LastError = supErr.Error()
		case !supervisor.BinaryAvailable():
			// No Apply attempted yet (no cached config) but the binary isn't
			// there either — don't claim healthy just because nothing has
			// failed out loud.
			h.Healthy = false
			h.Status = "collector binary missing"
			h.LastError = fmt.Sprintf("%v at %s", collector.ErrBinaryMissing, supervisor.BinaryPath())
		default:
			h.Healthy = true
			h.Status = "running"
		}
		return h
	}

	eff := &effectiveCfg{}
	var initialSupErr error
	if body, err := os.ReadFile(configPath); err == nil {
		// Repair perms on upgrade: pre-fix installs wrote 0o644. Tighten
		// to 0o600 here so existing hosts converge on the new policy as
		// soon as the agent restarts, without waiting for a remote push.
		_ = os.Chmod(configPath, 0o600)
		// Inject platform-specific defaults (e.g. mute_process_user_error)
		// that operators shouldn't need to know about. Write back only when
		// the patch changes something so the file mtime stays stable.
		if patched := patchConfig(body); len(patched) != len(body) || string(patched) != string(body) {
			if err := os.WriteFile(configPath, patched, 0o600); err == nil {
				body = patched
			}
		}
		sum := sha256.Sum256(body)
		eff.set(body, sum[:])
		logger.Info("loaded cached config", "path", configPath, "bytes", len(body))
		initialSupErr = supervisor.Apply(ctx)
		if initialSupErr != nil {
			logger.Error("initial collector start", "err", initialSupErr)
		}
	}

	c := client.NewWebSocket(opampLogger{logger: logger})

	if err := c.SetAgentDescription(agentDescription()); err != nil {
		return fmt.Errorf("set agent description: %w", err)
	}
	if err := c.SetHealth(buildHealth(initialSupErr)); err != nil {
		return fmt.Errorf("set health: %w", err)
	}

	caps := protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig |
		protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig
	if err := c.SetCapabilities(&caps); err != nil {
		return fmt.Errorf("set capabilities: %w", err)
	}

	// Pull the auth token from env. Empty string is acceptable — the
	// control plane runs in v0.1-compatible no-auth mode in that case.
	// We always set a Header value (possibly empty) so that the OpAMP
	// client uses our http.Header rather than its own default.
	authHeader := http.Header{}
	if token := os.Getenv("MAGPIE_API_TOKEN"); token != "" {
		authHeader.Set("Authorization", "Bearer "+token)
		logger.Info("opamp: bearer-token auth enabled")
	} else {
		logger.Warn("MAGPIE_API_TOKEN unset — connecting without auth (compatible with v0.1 magpied)")
	}

	settings := clienttypes.StartSettings{
		OpAMPServerURL: serverURL,
		Header:         authHeader,
		InstanceUid:    stableInstanceUID(logger),
		Callbacks: clienttypes.Callbacks{
			OnConnect: func(_ context.Context) {
				lastConnectUnix.Store(time.Now().Unix())
				logger.Info("opamp connected")
			},
			OnConnectFailed: func(_ context.Context, err error) {
				logger.Warn("opamp connect failed", "err", err)
			},
			OnError: func(_ context.Context, e *protobufs.ServerErrorResponse) {
				logger.Error("opamp server error", "err", e.GetErrorMessage())
			},
			GetEffectiveConfig: func(_ context.Context) (*protobufs.EffectiveConfig, error) {
				body, _ := eff.get()
				return &protobufs.EffectiveConfig{
					ConfigMap: &protobufs.AgentConfigMap{
						ConfigMap: map[string]*protobufs.AgentConfigFile{
							"": {Body: body, ContentType: "text/yaml"},
						},
					},
				}, nil
			},
			OnMessage: func(ctx context.Context, msg *clienttypes.MessageData) {
				// Log every dispatch unconditionally so we can confirm
				// from agent-side whether the server's RemoteConfig is
				// reaching this callback — the stuck-rollout pattern
				// where apply_state hangs at "applying" can be either
				// "server never pushed" or "agent never received"; this
				// log line pins down which side is at fault.
				rc := msg.RemoteConfig
				if rc == nil {
					logger.Debug("opamp: OnMessage with no RemoteConfig")
					return
				}
				if rc.Config == nil {
					logger.Info("opamp: received RemoteConfig with empty Config map", "hash", fmt.Sprintf("%x", rc.ConfigHash))
					return
				}
				logger.Info("opamp: received RemoteConfig", "hash", fmt.Sprintf("%x", rc.ConfigHash))
				body := patchConfig(extractYAML(rc.Config))
				// Tight perms: the config can embed OTLP tokens / scrape
				// credentials. 0o700 dir + 0o600 file keeps it readable
				// only by the agent's own user (root or LocalSystem in
				// the supported install paths).
				if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil && filepath.Dir(configPath) != "." {
					logger.Error("prepare config dir", "err", err)
				}
				if err := os.WriteFile(configPath, body, 0o600); err != nil {
					logger.Error("write config", "err", err)
					_ = c.SetRemoteConfigStatus(&protobufs.RemoteConfigStatus{
						LastRemoteConfigHash: rc.ConfigHash,
						Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED,
						ErrorMessage:         err.Error(),
					})
					return
				}
				eff.set(body, rc.ConfigHash)

				supervisorErr := supervisor.Apply(ctx)
				status := protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED
				errMsg := ""
				if supervisorErr != nil {
					logger.Error("apply to collector", "err", supervisorErr)
					status = protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED
					errMsg = supervisorErr.Error()
				}

				if err := c.SetRemoteConfigStatus(&protobufs.RemoteConfigStatus{
					LastRemoteConfigHash: rc.ConfigHash,
					Status:               status,
					ErrorMessage:         errMsg,
				}); err != nil {
					logger.Error("set remote config status", "err", err)
					return
				}
				// Health tracks collector-running state; refresh it on every
				// Apply so a FAILED config (e.g. binary missing after the
				// agent started healthy) flips the agent to unhealthy in the
				// UI instead of lingering as "green but broken".
				if err := c.SetHealth(buildHealth(supervisorErr)); err != nil {
					logger.Error("set health", "err", err)
				}
				if err := c.UpdateEffectiveConfig(ctx); err != nil {
					logger.Error("update effective config", "err", err)
				}
				logger.Info("config applied", "bytes", len(body), "hash", fmt.Sprintf("%x", rc.ConfigHash), "status", status.String())
			},
		},
	}

	if err := c.Start(ctx, settings); err != nil {
		return fmt.Errorf("opamp start: %w", err)
	}

	// Reconnect watchdog: opamp-go's client.NewWebSocket has internal
	// retry logic but has been observed to stall after a control-plane
	// restart, leaving the agent process alive but disconnected for
	// 20+ minutes. Watch the last-successful-connect timestamp; if
	// >5 min stale after we know we've connected once, exit. The
	// Windows service manager's recovery actions (configured in
	// internal/winservice/service_windows.go) restart the process,
	// which gets us a fresh OpAMP client on a clean reconnect path.
	go func() {
		const stale = 5 * time.Minute
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				last := lastConnectUnix.Load()
				if last == 0 {
					continue // never connected yet — let opamp-go keep trying
				}
				if now.Sub(time.Unix(last, 0)) > stale {
					logger.Error("opamp watchdog: connection stale, exiting for SCM-restart",
						"last_connect_age_seconds", int(now.Sub(time.Unix(last, 0)).Seconds()))
					os.Exit(1)
				}
			}
		}
	}()

	<-ctx.Done()
	logger.Info("magpie agent shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	supervisor.Shutdown(shutdownCtx)
	if err := c.Stop(shutdownCtx); err != nil {
		logger.Error("opamp stop", "err", err)
	}
	return nil
}

func agentDescription() *protobufs.AgentDescription {
	host, _ := os.Hostname()
	displayName := envOr("MAGPIE_AGENT_NAME", host)
	product := envOr("MAGPIE_PRODUCT", "default")
	variant := envOr("MAGPIE_VARIANT", runtime.GOOS)
	return &protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			stringAttr("service.name", "magpie-agent"),
			stringAttr("service.version", version),
			stringAttr("service.instance.id", displayName),
			stringAttr("magpie.product", product),
			stringAttr("magpie.variant", variant),
		},
		NonIdentifyingAttributes: []*protobufs.KeyValue{
			stringAttr("os.type", runtime.GOOS),
			stringAttr("host.arch", runtime.GOARCH),
			stringAttr("host.name", displayName),
			stringAttr("host.hostname", host),
		},
	}
}

// extractYAML flattens AgentConfigMap into a single YAML body. Magpie currently
// uses a single unnamed config file; if the server ever sends multiple, we
// concatenate them with "---" document separators.
func extractYAML(m *protobufs.AgentConfigMap) []byte {
	if m == nil {
		return nil
	}
	if f, ok := m.ConfigMap[""]; ok && f != nil {
		return f.Body
	}
	var out []byte
	first := true
	for _, f := range m.ConfigMap {
		if f == nil {
			continue
		}
		if !first {
			out = append(out, []byte("\n---\n")...)
		}
		out = append(out, f.Body...)
		first = false
	}
	return out
}

func stringAttr(k, v string) *protobufs.KeyValue {
	return &protobufs.KeyValue{
		Key:   k,
		Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: v}},
	}
}

// stableInstanceUID returns the InstanceUid this agent presents to the
// control-plane. It is derived deterministically from a per-host seed
// (machineid.Seed) so a given physical machine always upserts into the
// same registry row across agent restarts, reboots, and reinstalls.
//
// Why this matters: the control-plane keys the agents table by InstanceUid
// alone. Before this change the UID was random per process, so every
// restart created a new "host" row in the UI — operators saw 4+ duplicate
// rows for a single laptop. With a stable seed, the row is reused.
//
// If the platform's seed source is unreadable (rare — e.g. a stripped
// container with no /etc/machine-id) we fall back to a random UID and log
// a warning. Behavior in that case matches the old code, so we never
// regress, only fail to improve.
func stableInstanceUID(logger *slog.Logger) clienttypes.InstanceUid {
	seed, err := machineid.Seed()
	if err != nil || seed == "" {
		logger.Warn("machine-id unavailable; falling back to random InstanceUid (host may duplicate on restart)", "err", err)
		return newInstanceUID()
	}
	// Domain-separation prefix: ensures the same machine-id used by
	// other tools yields a different UUID here, so leaking our UID
	// doesn't leak the underlying machine-id.
	sum := sha256.Sum256([]byte("magpie-agent/instance-uid\x00" + seed))
	var uid clienttypes.InstanceUid
	copy(uid[:], sum[:16])
	uid[6] = (uid[6] & 0x0f) | 0x40 // RFC 4122 version 4 bits
	uid[8] = (uid[8] & 0x3f) | 0x80 // RFC 4122 variant bits
	return uid
}

func newInstanceUID() clienttypes.InstanceUid {
	var uid clienttypes.InstanceUid
	_, _ = rand.Read(uid[:])
	uid[6] = (uid[6] & 0x0f) | 0x40
	uid[8] = (uid[8] & 0x3f) | 0x80
	return uid
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// normalizeServerURL turns whatever the operator (or installer) passed
// as -server into a canonical OpAMP WebSocket URL. Accepted shapes:
//
//	bare host or IP             -> ws://<host>:12002/v1/opamp
//	  (e.g. "10.0.0.5", "magpied.internal")
//	http://host[:port][/path]   -> ws://host[:port]/v1/opamp        (preserves port; ignores path)
//	https://host[:port][/path]  -> wss://host[:port]/v1/opamp       (preserves TLS + port)
//	ws://...  /  wss://...      -> as-is (operator has full control)
//
// The http(s) cases exist because the install script bakes in the
// magpied URL the script was downloaded from (e.g.
// https://magpie.example.com). Without this normalization, the
// previous "starts with ws://?" check produced
// ws://https://magpie.example.com:12002/v1/opamp — a malformed URL
// that the agent would silently fail to dial, leaving the host
// invisible in the UI.
func normalizeServerURL(in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", errors.New("empty value")
	}
	// Already a websocket URL: trust the operator entirely.
	if strings.HasPrefix(in, "ws://") || strings.HasPrefix(in, "wss://") {
		return in, nil
	}
	// Bare host / host:port (no scheme): default to ws on 12002.
	if !strings.Contains(in, "://") {
		return "ws://" + in + ":12002/v1/opamp", nil
	}
	u, err := url.Parse(in)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported scheme %q (want http, https, ws, wss, or a bare host)", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("missing host")
	}
	// Always anchor at /v1/opamp — operators using a custom prefix can
	// pass a ws:// URL directly, which we leave alone above.
	u.Path = "/v1/opamp"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// opampLogger adapts *slog.Logger to opamp-go's client/types.Logger.
type opampLogger struct{ logger *slog.Logger }

func (l opampLogger) Debugf(_ context.Context, format string, v ...any) {
	l.logger.Debug("opamp", "detail", fmt.Sprintf(format, v...))
}

func (l opampLogger) Errorf(_ context.Context, format string, v ...any) {
	l.logger.Error("opamp", "detail", fmt.Sprintf(format, v...))
}
