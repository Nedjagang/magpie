package opamp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server"
	"github.com/open-telemetry/opamp-go/server/types"
)

// ConfigProvider resolves the desired YAML for a connected agent.
//
// As of v1.0 the resolver is rollout-aware: the implementation in
// cmd/magpied/main.go consults rollouts.Service.ResolveConfigFor first
// (which checks for in-flight rollouts that target this host, then
// live Configs from completed rollouts, then v0.2 fallback). The
// instance_uid argument is what makes Shape 1 per-host overrides work.
//
// Returns ok=false if no config is available even after the full
// resolution chain.
type ConfigProvider interface {
	ResolveFor(ctx context.Context, instanceUID, product, variant string) (yaml string, ok bool, err error)
}

// HeartbeatHook is called once per AgentToServer message after the
// registry has been updated. v1.0 wires rollouts.Service.HandleHeartbeat
// here so apply_state rows transition to applied/failed based on what
// the agent reports it's running. Optional — Server runs unchanged when
// no hook is set.
//
// instanceUID is the hex-encoded uid (matching registry keys).
// appliedHash is hex-encoded sha256 of the config the agent reports
// applied (empty if the agent hasn't reported an applied config yet).
// configFailed mirrors RemoteConfigStatus.Status == FAILED.
// errorMessage mirrors RemoteConfigStatus.ErrorMessage.
type HeartbeatHook interface {
	HandleHeartbeat(ctx context.Context, instanceUID, appliedHash string, configFailed bool, errorMessage string) error
}

// Server wraps an opamp-go server and exposes an HTTP handler that can be
// mounted on any existing mux.
type Server struct {
	logger    *slog.Logger
	registry  *Registry
	configs   ConfigProvider
	heartbeat HeartbeatHook
	inner     server.OpAMPServer
	handler   server.HTTPHandlerFunc
}

// SetHeartbeatHook installs a hook that fires for every AgentToServer
// message after the registry update. Pass nil to disable.
func (s *Server) SetHeartbeatHook(h HeartbeatHook) { s.heartbeat = h }

func NewServer(logger *slog.Logger, registry *Registry, configs ConfigProvider) (*Server, error) {
	s := &Server{
		logger:   logger,
		registry: registry,
		configs:  configs,
	}
	s.inner = server.New(slogAdapter{logger: logger})

	connCallbacks := types.ConnectionCallbacks{
		OnConnected: func(_ context.Context, _ types.Connection) {
			logger.Info("opamp agent connected")
		},
		OnMessage: s.onMessage,
		OnConnectionClose: func(conn types.Connection) {
			s.registry.Disconnect(conn)
			logger.Info("opamp agent disconnected")
		},
	}

	handler, _, err := s.inner.Attach(server.Settings{
		Callbacks: types.Callbacks{
			OnConnecting: func(_ *http.Request) types.ConnectionResponse {
				return types.ConnectionResponse{
					Accept:              true,
					ConnectionCallbacks: connCallbacks,
				}
			},
		},
	})
	if err != nil {
		return nil, err
	}
	s.handler = handler
	return s, nil
}

// Handler returns the HTTP handler to mount at the OpAMP path (typically /v1/opamp).
func (s *Server) Handler() http.HandlerFunc {
	return http.HandlerFunc(s.handler)
}

// Reconcile walks every connected agent, resolves the config for its
// (product, variant), and pushes it if it differs from what the agent has
// applied. Intended to be called after a config is created or a rollout
// transitions, so agents pick it up without waiting for the next heartbeat.
//
// As of v1.0, ResolveFor takes the agent's instance_uid so the resolver
// can prefer in-flight rollouts (Shape 1 per-host overrides + ongoing
// product+variant rollouts) over the live Config for the agent's Scope.
func (s *Server) Reconcile(ctx context.Context) {
	for _, ref := range s.registry.Connections() {
		product, variant := s.registry.Labels(ref.InstanceUID)
		yaml, ok, err := s.configs.ResolveFor(ctx, ref.InstanceUID, product, variant)
		if err != nil {
			s.logger.Error("reconcile: resolve", "uid", ref.InstanceUID, "err", err)
			continue
		}
		if !ok {
			continue
		}
		sum := sha256.Sum256([]byte(yaml))
		hash := sum[:]
		if prior := s.registry.AppliedHash(ref.InstanceUID); prior != nil && bytes.Equal(prior, hash) {
			continue
		}
		msg := &protobufs.ServerToAgent{
			RemoteConfig: &protobufs.AgentRemoteConfig{
				Config: &protobufs.AgentConfigMap{
					ConfigMap: map[string]*protobufs.AgentConfigFile{
						"": {Body: []byte(yaml), ContentType: "text/yaml"},
					},
				},
				ConfigHash: hash,
			},
		}
		if err := ref.Conn.Send(ctx, msg); err != nil {
			s.logger.Warn("reconcile: send failed", "uid", ref.InstanceUID, "err", err)
		}
	}
}

// RepushForUID forces a re-send of the currently-resolved config for a
// single agent, bypassing the "agent already reports this hash applied"
// short-circuit that Reconcile uses. Operators trigger this when an
// apply_state has been stuck in "applying" — the server's normal flow
// suppresses redundant pushes, which is wrong when the agent never
// dispatched the prior push to its OnMessage callback.
//
// Returns (false, nil) if the agent isn't connected, (false, err) on a
// resolve / send failure, (true, nil) on a successful WebSocket send.
// The agent is still allowed to be a no-op on receive (e.g. its
// opamp-go client may suppress duplicate dispatches), but at the wire
// level the push has been retried.
func (s *Server) RepushForUID(ctx context.Context, instanceUID string) (sent bool, err error) {
	var conn types.Connection
	for _, ref := range s.registry.Connections() {
		if ref.InstanceUID == instanceUID {
			conn = ref.Conn
			break
		}
	}
	if conn == nil {
		return false, nil
	}
	product, variant := s.registry.Labels(instanceUID)
	yaml, ok, err := s.configs.ResolveFor(ctx, instanceUID, product, variant)
	if err != nil {
		return false, fmt.Errorf("resolve: %w", err)
	}
	if !ok {
		return false, fmt.Errorf("no config resolved for %s/%s", product, variant)
	}
	sum := sha256.Sum256([]byte(yaml))
	hash := sum[:]
	msg := &protobufs.ServerToAgent{
		RemoteConfig: &protobufs.AgentRemoteConfig{
			Config: &protobufs.AgentConfigMap{
				ConfigMap: map[string]*protobufs.AgentConfigFile{
					"": {Body: []byte(yaml), ContentType: "text/yaml"},
				},
			},
			ConfigHash: hash,
		},
	}
	if err := conn.Send(ctx, msg); err != nil {
		return false, fmt.Errorf("send: %w", err)
	}
	s.logger.Info("opamp: re-push for uid", "uid", instanceUID, "hash", hex.EncodeToString(hash))
	return true, nil
}

func (s *Server) onMessage(ctx context.Context, conn types.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	s.registry.Upsert(ctx, conn, msg)

	resp := &protobufs.ServerToAgent{InstanceUid: msg.InstanceUid}
	uid := hex.EncodeToString(msg.InstanceUid)

	// v1.0: heartbeat hook fires after registry update so it sees the
	// freshest agent state. Errors are logged but don't block the
	// config-resolve path — the apply_state will reconcile on the next
	// heartbeat or AdvancePhase tick.
	if s.heartbeat != nil {
		var (
			appliedHex string
			failed     bool
			errMsg     string
		)
		if st := msg.RemoteConfigStatus; st != nil {
			if len(st.LastRemoteConfigHash) > 0 {
				appliedHex = hex.EncodeToString(st.LastRemoteConfigHash)
			}
			failed = st.Status == protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED
			errMsg = st.ErrorMessage
		}
		if err := s.heartbeat.HandleHeartbeat(ctx, uid, appliedHex, failed, errMsg); err != nil {
			s.logger.Error("heartbeat hook", "uid", uid, "err", err)
		}
	}

	product, variant := s.registry.Labels(uid)
	yaml, ok, err := s.configs.ResolveFor(ctx, uid, product, variant)
	if err != nil {
		s.logger.Error("resolve config", "uid", uid, "product", product, "variant", variant, "err", err)
		return resp
	}
	if !ok {
		return resp
	}

	sum := sha256.Sum256([]byte(yaml))
	hash := sum[:]

	// Skip resend if the agent already reports this exact hash applied —
	// either in the current message, or in prior messages we've recorded.
	if st := msg.RemoteConfigStatus; st != nil && bytes.Equal(st.LastRemoteConfigHash, hash) {
		return resp
	}
	if prior := s.registry.AppliedHash(uid); prior != nil && bytes.Equal(prior, hash) {
		return resp
	}

	resp.RemoteConfig = &protobufs.AgentRemoteConfig{
		Config: &protobufs.AgentConfigMap{
			ConfigMap: map[string]*protobufs.AgentConfigFile{
				"": {
					Body:        []byte(yaml),
					ContentType: "text/yaml",
				},
			},
		},
		ConfigHash: hash,
	}
	s.logger.Info("opamp: pushing remote config",
		"uid", uid,
		"product", product, "variant", variant,
		"yaml_bytes", len(yaml),
		"hash", hex.EncodeToString(hash))
	return resp
}

// slogAdapter adapts *slog.Logger to the opamp-go types.Logger interface.
type slogAdapter struct{ logger *slog.Logger }

func (a slogAdapter) Debugf(_ context.Context, format string, v ...any) {
	a.logger.Debug("opamp", "detail", fmt.Sprintf(format, v...))
}

func (a slogAdapter) Errorf(_ context.Context, format string, v ...any) {
	a.logger.Error("opamp", "detail", fmt.Sprintf(format, v...))
}
