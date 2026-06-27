// Package opamp wires the OpAMP server and tracks connected agents in memory.
package opamp

import (
	"context"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

// LabelOverrider resolves server-side (product, variant) overrides for an
// agent. Returned ok=false means no override exists and the registry should
// fall back to whatever the agent advertised.
type LabelOverrider interface {
	Override(instanceUID string) (product, variant string, ok bool)
}

// AgentPersister lets the registry survive a magpied restart: Upsert is
// called on every OpAMP message, All is called once at startup to
// hydrate the in-memory map. Implementations live in internal/agents.
// nil is a valid value — the registry runs in-memory-only (useful for
// tests; production wires a real store in main.go).
type AgentPersister interface {
	Upsert(ctx context.Context, a *Agent) error
	All(ctx context.Context) (map[string]*Agent, error)
}

type Agent struct {
	InstanceUID      string            `json:"instance_uid"`
	Attributes       map[string]string `json:"attributes,omitempty"`
	Healthy          *bool             `json:"healthy,omitempty"`
	LastStatus       string            `json:"last_status,omitempty"`
	ConnectedAt      time.Time         `json:"connected_at"`
	LastSeen         time.Time         `json:"last_seen"`
	AppliedConfigHex string            `json:"applied_config_hash,omitempty"`
	ConfigStatus     string            `json:"config_status,omitempty"`
	ConfigError      string            `json:"config_error,omitempty"`

	// Effective labels: what the control plane resolves for this agent,
	// taking server-side overrides into account. Populated at read time.
	EffectiveProduct string `json:"effective_product,omitempty"`
	EffectiveVariant string `json:"effective_variant,omitempty"`
	OverrideActive   bool   `json:"label_override,omitempty"`

	// Connected reflects whether the agent has an open OpAMP WebSocket
	// right now — derived from the in-memory r.conns map at read time,
	// not the persisted last_seen. Distinguishes "disconnected since
	// magpied restarted" (healthy=true, last_seen=stale, connected=false)
	// from "running and chatty" (healthy=true, last_seen=fresh, connected=true).
	// Not persisted; the JSON field is populated by registry.List/Get.
	Connected bool `json:"connected"`
}

type Registry struct {
	mu     sync.RWMutex
	agents map[string]*Agent           // keyed by hex(InstanceUid)
	conns  map[types.Connection]string // connection -> instance uid (for disconnect)

	overrider LabelOverrider
	persister AgentPersister
	logger    *slog.Logger // used only to report persister errors; never on the hot path
}

func NewRegistry() *Registry {
	return &Registry{
		agents: make(map[string]*Agent),
		conns:  make(map[types.Connection]string),
	}
}

// SetOverrider installs the label-override resolver. Called once at startup
// after the labels Store is constructed. Passing nil disables overrides.
func (r *Registry) SetOverrider(o LabelOverrider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overrider = o
}

// SetPersister installs the DB-backed store so the registry survives
// magpied restarts. Logger is optional — used only to report persist
// failures without interrupting the OpAMP message path. Call this
// BEFORE the OpAMP server starts accepting connections.
func (r *Registry) SetPersister(p AgentPersister, logger *slog.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.persister = p
	r.logger = logger
}

// Warm loads every persisted agent into the in-memory map. Safe to call
// even with no persister installed (no-op). Call at startup, after
// SetPersister and before the HTTP server accepts connections, so the
// UI's Hosts view has content the moment magpied comes back up.
//
// On error we return it — main.go can decide whether to fail-hard or
// proceed with an empty in-memory map (in practice, proceed: an empty
// UI for a few seconds beats a failed restart).
func (r *Registry) Warm(ctx context.Context) error {
	r.mu.RLock()
	p := r.persister
	r.mu.RUnlock()
	if p == nil {
		return nil
	}
	loaded, err := p.All(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Merge, don't replace: if an agent reconnected between SetPersister
	// and Warm, keep its live state.
	for uid, a := range loaded {
		if _, exists := r.agents[uid]; !exists {
			r.agents[uid] = a
		}
	}
	return nil
}

// Upsert folds an OpAMP AgentToServer message into both the in-memory
// map and (if a persister is installed) the database. The ctx governs
// only the DB write — the in-memory update always succeeds because
// losing the in-memory state on a failed persist would make the UI
// diverge from reality.
func (r *Registry) Upsert(ctx context.Context, conn types.Connection, msg *protobufs.AgentToServer) {
	uid := hex.EncodeToString(msg.InstanceUid)
	if uid == "" {
		return
	}
	now := time.Now().UTC()

	r.mu.Lock()
	a, ok := r.agents[uid]
	if !ok {
		a = &Agent{InstanceUID: uid, ConnectedAt: now}
		r.agents[uid] = a
	}
	a.LastSeen = now
	r.conns[conn] = uid

	if desc := msg.AgentDescription; desc != nil {
		attrs := make(map[string]string, len(desc.IdentifyingAttributes)+len(desc.NonIdentifyingAttributes))
		for _, kv := range desc.IdentifyingAttributes {
			if v := kv.GetValue().GetStringValue(); v != "" {
				attrs[kv.Key] = v
			}
		}
		for _, kv := range desc.NonIdentifyingAttributes {
			if v := kv.GetValue().GetStringValue(); v != "" {
				attrs[kv.Key] = v
			}
		}
		if len(attrs) > 0 {
			a.Attributes = attrs
		}
	}
	if h := msg.Health; h != nil {
		healthy := h.Healthy
		a.Healthy = &healthy
		a.LastStatus = h.Status
	}
	if s := msg.RemoteConfigStatus; s != nil {
		a.AppliedConfigHex = hex.EncodeToString(s.LastRemoteConfigHash)
		a.ConfigStatus = s.Status.String()
		a.ConfigError = s.ErrorMessage
	}

	// Snapshot the Agent under the lock so the DB write below sees a
	// consistent view without holding the mutex during I/O.
	snap := *a
	persister := r.persister
	logger := r.logger
	r.mu.Unlock()

	if persister != nil {
		if err := persister.Upsert(ctx, &snap); err != nil && logger != nil {
			// Log and move on: a failed persist must not propagate to
			// the OpAMP response (that would drop agent heartbeats).
			// The next message retries, so transient DB errors are
			// self-healing.
			logger.Warn("persist agent state", "uid", uid, "err", err)
		}
	}
}

// Labels returns the effective (product, variant) for an agent. Checks
// server-side overrides first; falls back to advertised attributes, then to
// "default"/"default".
func (r *Registry) Labels(instanceUID string) (product, variant string) {
	r.mu.RLock()
	overrider := r.overrider
	r.mu.RUnlock()
	if overrider != nil {
		if p, v, ok := overrider.Override(instanceUID); ok {
			return p, v
		}
	}
	return r.advertisedLabels(instanceUID)
}

func (r *Registry) advertisedLabels(instanceUID string) (product, variant string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	product, variant = "default", "default"
	a, ok := r.agents[instanceUID]
	if !ok {
		return
	}
	if v, ok := a.Attributes["magpie.product"]; ok && v != "" {
		product = v
	}
	if v, ok := a.Attributes["magpie.variant"]; ok && v != "" {
		variant = v
	}
	return
}

// Remove drops an agent from the in-memory map. Used by the admin
// "delete host" flow so operators can clear stale rows left behind
// before InstanceUid was deterministic. Returns true if a row was
// removed.
//
// Note: this only touches in-memory state. The DB row is removed
// separately by the HTTP handler (via agents.Store.Delete) so the two
// can fail independently — losing one but not the other is recoverable
// at next restart (Warm re-hydrates from disk).
//
// If the agent is still connected, its next OpAMP message will recreate
// the row; that's intentional — Remove is for stale entries, and a live
// agent with a deterministic InstanceUid will land back in the same uid
// it had before, not duplicate.
func (r *Registry) Remove(instanceUID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[instanceUID]; !ok {
		return false
	}
	delete(r.agents, instanceUID)
	// Drop any conn -> uid mapping pointing at this agent so a later
	// Disconnect() doesn't carry a dangling reference.
	for c, uid := range r.conns {
		if uid == instanceUID {
			delete(r.conns, c)
		}
	}
	return true
}

func (r *Registry) Disconnect(conn types.Connection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.conns, conn)
}

// Connections returns a snapshot of (connection, instanceUID) pairs for all agents
// currently holding an open websocket.
func (r *Registry) Connections() []ConnRef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ConnRef, 0, len(r.conns))
	for c, uid := range r.conns {
		out = append(out, ConnRef{Conn: c, InstanceUID: uid})
	}
	return out
}

type ConnRef struct {
	Conn        types.Connection
	InstanceUID string
}

// AppliedHash returns the last remote-config hash the agent reported applying.
// Returns nil if the agent is unknown or has not reported a hash yet.
func (r *Registry) AppliedHash(instanceUID string) []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[instanceUID]
	if !ok || a.AppliedConfigHex == "" {
		return nil
	}
	b, err := hex.DecodeString(a.AppliedConfigHex)
	if err != nil {
		return nil
	}
	return b
}

// Get returns a snapshot copy of a single agent. Effective labels and override
// state are computed at call time.
func (r *Registry) Get(instanceUID string) (Agent, bool) {
	r.mu.RLock()
	a, ok := r.agents[instanceUID]
	if !ok {
		r.mu.RUnlock()
		return Agent{}, false
	}
	cp := *a
	overrider := r.overrider
	connected := false
	for _, uid := range r.conns {
		if uid == instanceUID {
			connected = true
			break
		}
	}
	r.mu.RUnlock()

	cp.Connected = connected

	// Fill effective labels without holding the lock (Labels takes its own).
	p, v := r.advertisedLabels(instanceUID)
	cp.EffectiveProduct, cp.EffectiveVariant = p, v
	if overrider != nil {
		if op, ov, has := overrider.Override(instanceUID); has {
			cp.EffectiveProduct, cp.EffectiveVariant = op, ov
			cp.OverrideActive = true
		}
	}
	return cp, true
}

func (r *Registry) List() []*Agent {
	r.mu.RLock()
	overrider := r.overrider
	uids := make([]string, 0, len(r.agents))
	snapshot := make(map[string]Agent, len(r.agents))
	for uid, a := range r.agents {
		uids = append(uids, uid)
		snapshot[uid] = *a
	}
	connected := make(map[string]struct{}, len(r.conns))
	for _, uid := range r.conns {
		connected[uid] = struct{}{}
	}
	r.mu.RUnlock()

	out := make([]*Agent, 0, len(uids))
	for _, uid := range uids {
		a := snapshot[uid]
		_, isConn := connected[uid]
		a.Connected = isConn
		p, v := r.advertisedLabels(uid)
		a.EffectiveProduct, a.EffectiveVariant = p, v
		if overrider != nil {
			if op, ov, ok := overrider.Override(uid); ok {
				a.EffectiveProduct, a.EffectiveVariant = op, ov
				a.OverrideActive = true
			}
		}
		out = append(out, &a)
	}
	return out
}
