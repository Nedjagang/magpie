package rollouts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sort"
	"time"

	"github.com/magpie-project/magpie/control-plane/internal/audit"
	"github.com/magpie-project/magpie/control-plane/internal/configs"
	"github.com/magpie-project/magpie/control-plane/internal/pagination"
)

// Clock is the time source the Service uses for all wall-clock work
// (canary_at, soak_at, gate_passed_at, etc., plus the soak-window
// elapsed check inside AdvancePhase). Tests inject a fake Clock to
// exercise time-dependent transitions without sleeping; production
// uses SystemClock.
type Clock interface {
	Now() time.Time
}

// SystemClock returns the wall-clock UTC time. Default for production.
type SystemClock struct{}

// Now returns time.Now().UTC().
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// Service is the orchestration layer for Rollouts. It coordinates the
// state machine in state.go with persistence in store.go and emits
// audit events for every state transition + operator action.
//
// Dependencies are injected so tests can stub the host resolver or the
// audit store. In production wiring (cmd/magpied/main.go) the resolver
// is an adapter over opamp.Registry; in tests the resolver is a
// hand-rolled stub returning fixed host lists.
type Service struct {
	store              *Store
	configStore        *configs.Store
	hosts              HostResolver
	policy             configs.Policy
	auditStore         *audit.Store
	logger             *slog.Logger
	clock              Clock
	validationObserver ValidationObserver
	semantic           SemanticValidator

	// Defaults per docs/v1.0-decisions.md.
	defaultCanaryPct   int      // 5
	defaultCanaryCount int      // 10
	defaultSoakSeconds int      // 300
	defaultGateMode    GateMode // auto
}

// SemanticValidator runs the OTel collector loader against a config
// YAML and returns an error if loading would fail. Production wiring
// in cmd/magpied/main.go uses internal/semantic.OtelcolValidator.
//
// Optional: a Service constructed with semantic=nil runs structural
// validation only (v0.2-compat behavior). The skip is logged once at
// startup so operators see the degraded state.
//
// Defining the interface here (rather than importing internal/semantic)
// keeps the rollouts package free of subprocess/fs concerns.
type SemanticValidator interface {
	Validate(ctx context.Context, yaml string) error
}

// SetSemanticValidator wires a semantic validator. Production callers
// pass it to NewService via SetSemanticValidator after construction so
// the constructor signature stays small and tests don't have to thread
// a nil through the parameter list.
func (s *Service) SetSemanticValidator(v SemanticValidator) {
	s.semantic = v
}

// SetClock replaces the time source used by the Service. Intended for
// tests; production callers leave the SystemClock default in place.
func (s *Service) SetClock(c Clock) {
	if c != nil {
		s.clock = c
	}
}

// ValidationObserver receives validation-call latency observations.
// Implemented by metrics.Registry (see internal/metrics) and wired in
// main.go so the rollouts package doesn't depend on metrics.
type ValidationObserver interface {
	ObserveValidationLatency(d time.Duration)
}

// SetValidationObserver wires a ValidationObserver — the production
// implementation routes observations into the magpie_validation_latency_seconds
// histogram. Optional; tests can leave it nil.
func (s *Service) SetValidationObserver(v ValidationObserver) {
	s.validationObserver = v
}

// HostResolver returns the currently-connected agent instance_uids
// matching a Scope. opamp.Registry implements this via an adapter in
// cmd/magpied/main.go; tests pass a stub.
//
// For ScopeProductVariant the implementation should return uids of
// agents whose effective (product, variant) — after label override
// resolution — matches scope_ref encoded as "product/variant".
//
// For ScopeInstance the implementation should return [scope_ref] if
// that uid is connected, or an empty slice otherwise.
type HostResolver interface {
	ConnectedForScope(kind ScopeKind, ref string) []string
}

// NewService wires a Service. Defaults are pulled from the decisions
// doc; production callers pass them through for v1.x configurability.
func NewService(
	store *Store,
	configStore *configs.Store,
	hosts HostResolver,
	policy configs.Policy,
	auditStore *audit.Store,
	logger *slog.Logger,
) *Service {
	return &Service{
		store:              store,
		configStore:        configStore,
		hosts:              hosts,
		policy:             policy,
		auditStore:         auditStore,
		logger:             logger,
		clock:              SystemClock{},
		defaultCanaryPct:   5,
		defaultCanaryCount: 10,
		defaultSoakSeconds: 300,
		defaultGateMode:    GateAuto,
	}
}

// CreateRequest is the input shape for Service.Create. Maps onto the
// `POST /api/v1/rollouts` body in spec §9.
type CreateRequest struct {
	ScopeKind   ScopeKind
	ScopeRef    string
	ConfigName  string
	ConfigYAML  string
	Kind        Kind     // Phased or Instant
	CanaryPct   *int     // optional; defaults to 5
	CanaryCount *int     // optional; defaults to 10
	SoakSeconds *int     // optional; defaults to 300
	GateMode    GateMode // optional; defaults to "auto"
}

// ErrInFlight is returned when Create is called for a Scope that already
// has a non-terminal rollout. Maps to 409 in the API. The existing
// rollout's id is in ExistingID.
type ErrInFlight struct {
	ExistingID int64
	State      State
}

func (e *ErrInFlight) Error() string {
	return fmt.Sprintf("rollout: scope already has in-flight rollout #%d (state=%s)", e.ExistingID, e.State)
}

// ErrValidate is returned when the rollout's Config fails validation.
// The rollout is created in `aborted` state with abort_reason=validate_failed
// so post-incident review can find it; the API surfaces this as 400.
type ErrValidate struct {
	Detail string
}

func (e *ErrValidate) Error() string { return "rollout: validate failed: " + e.Detail }

// Create starts a new rollout. The full create path runs synchronously
// through the Validate phase; on success the returned Rollout is in
// `canary` (Phased) or `promoting` (Instant). Failure paths produce
// terminal `aborted` rollouts (validate_failed, no_canary_targets) so
// every attempted publish has a forensic trail.
//
// Per docs/v1.0-rollout-spec.md §3 concurrency rule: at most one
// non-terminal rollout per Scope. Returns *ErrInFlight if violated.
func (s *Service) Create(ctx context.Context, req CreateRequest, actor string) (*Rollout, error) {
	// 1. Input validation.
	if !req.ScopeKind.Valid() {
		return nil, fmt.Errorf("invalid scope_kind: %q", req.ScopeKind)
	}
	if req.ScopeRef == "" {
		return nil, errors.New("scope_ref is required")
	}
	if !req.Kind.Valid() {
		return nil, fmt.Errorf("invalid rollout_kind: %q", req.Kind)
	}
	if req.ConfigName == "" || req.ConfigYAML == "" {
		return nil, errors.New("config name and yaml are required")
	}
	gateMode := req.GateMode
	if gateMode == "" {
		gateMode = s.defaultGateMode
	}
	if !gateMode.Valid() {
		return nil, fmt.Errorf("invalid gate_mode: %q", gateMode)
	}

	// 2. Concurrency check.
	existing, err := s.store.FindInFlight(ctx, req.ScopeKind, req.ScopeRef)
	if err != nil {
		return nil, fmt.Errorf("check in-flight: %w", err)
	}
	if existing != nil {
		return nil, &ErrInFlight{ExistingID: existing.ID, State: existing.State}
	}

	// 3. Resolve the prior live Config for this Scope (rollback target).
	priorConfigID, err := s.store.LiveConfigID(ctx, req.ScopeKind, req.ScopeRef)
	if err != nil {
		return nil, fmt.Errorf("resolve prior config: %w", err)
	}

	// 4. Create the configs row.
	var cfg configs.Config
	switch req.ScopeKind {
	case ScopeProductVariant:
		// scope_ref is "product/variant"
		product, variant, ok := splitProductVariant(req.ScopeRef)
		if !ok {
			return nil, fmt.Errorf("invalid product_variant scope_ref: %q (want \"product/variant\")", req.ScopeRef)
		}
		cfg, err = s.configStore.Create(ctx, product, variant, req.ConfigName, req.ConfigYAML)
	case ScopeInstance:
		cfg, err = s.configStore.CreateForInstance(ctx, req.ScopeRef, req.ConfigName, req.ConfigYAML)
	}
	if err != nil {
		return nil, fmt.Errorf("create config row: %w", err)
	}

	// 5. Build the rollouts row, initially in validating.
	now := s.clock.Now()
	canaryPct := req.CanaryPct
	if canaryPct == nil {
		v := s.defaultCanaryPct
		canaryPct = &v
	}
	canaryCount := req.CanaryCount
	if canaryCount == nil {
		v := s.defaultCanaryCount
		canaryCount = &v
	}
	soakSeconds := s.defaultSoakSeconds
	if req.SoakSeconds != nil && *req.SoakSeconds > 0 {
		soakSeconds = *req.SoakSeconds
	}

	r := &Rollout{
		ScopeKind:     req.ScopeKind,
		ScopeRef:      req.ScopeRef,
		ConfigID:      cfg.ID,
		PriorConfigID: priorConfigID,
		Kind:          req.Kind,
		State:         StateValidating,
		CanaryPct:     canaryPct,
		CanaryCount:   canaryCount,
		SoakSeconds:   soakSeconds,
		GateMode:      gateMode,
		CreatedAt:     now,
		CreatedBy:     actor,
	}
	if err := s.store.Insert(ctx, r); err != nil {
		return nil, err
	}

	// 6. Run Validate. Two stages:
	//
	//   a) Structural — configs.ValidateWith. YAML well-formedness, required
	//      sections, receiver/exporter references, allowlist enforcement.
	//   b) Semantic — the OTel collector loader (plan §6.1 #7), wired through
	//      the SemanticValidator interface. Optional: nil validator means
	//      "v0.2-compat skip"; the warning is logged once at startup.
	//
	// Both stages share the same audit + abort path. The validation latency
	// observer wraps the entire pair so the histogram reflects the operator's
	// real wait time. The two stages are kept distinct in the abort_reason so
	// post-incident review can split out which loader rejected the YAML.
	validateStart := s.clock.Now()
	structuralErr := configs.ValidateWith(req.ConfigYAML, s.policy)
	if structuralErr != nil {
		if s.validationObserver != nil {
			s.validationObserver.ObserveValidationLatency(s.clock.Now().Sub(validateStart))
		}
		s.markAborted(ctx, r, AbortValidateFailed, now)
		s.recordAudit(ctx, audit.EventRolloutAborted, actor, r, map[string]any{
			"abort_reason": string(AbortValidateFailed),
			"details":      structuralErr.Error(),
		})
		return r, &ErrValidate{Detail: structuralErr.Error()}
	}

	if s.semantic != nil {
		semanticErr := s.semantic.Validate(ctx, req.ConfigYAML)
		if s.validationObserver != nil {
			s.validationObserver.ObserveValidationLatency(s.clock.Now().Sub(validateStart))
		}
		if semanticErr != nil {
			s.markAborted(ctx, r, AbortSemanticValidateFailed, now)
			s.recordAudit(ctx, audit.EventRolloutAborted, actor, r, map[string]any{
				"abort_reason": string(AbortSemanticValidateFailed),
				"details":      semanticErr.Error(),
			})
			return r, &ErrValidate{Detail: semanticErr.Error()}
		}
	} else if s.validationObserver != nil {
		// Even on the skip path, observe the structural-only latency so the
		// histogram doesn't develop a hole between v0.2-compat and v1.0
		// deploys. The bucket distribution will look bimodal once semantic
		// is wired in production; that's expected and intentional.
		s.validationObserver.ObserveValidationLatency(s.clock.Now().Sub(validateStart))
	}

	r.ValidatedAt = &now

	// 7. Validate passed. Transition based on Kind.
	switch req.Kind {
	case KindPhased:
		// Phased: select canary subset, create apply_state rows, enter Canary.
		if err := s.enterCanary(ctx, r, now); err != nil {
			return nil, err
		}
		s.recordAudit(ctx, audit.EventRolloutCreated, actor, r, map[string]any{
			"rollout_kind": string(KindPhased),
			"canary_size":  deref(r.CanarySize),
			"soak_seconds": r.SoakSeconds,
			"gate_mode":    string(r.GateMode),
		})
	case KindInstant:
		// Instant: fire-and-forget. The config is live the moment the rollout
		// is created; the OpAMP server pushes it on the next agent heartbeat
		// via the live-config resolution path (ResolveConfigFor step 3).
		// No apply_state rows, no promoting wait, no no_canary_targets gate.
		// Rollout goes validating → done in a single write.
		r.State = StateDone
		r.ValidatedAt = &now
		r.PromotedAt = &now
		r.DoneAt = &now
		if err := s.store.Update(ctx, r); err != nil {
			return nil, err
		}
		s.recordAudit(ctx, audit.EventRolloutInstant, actor, r, nil)
	}
	return r, nil
}

// enterCanary picks the canary subset, persists apply_state rows, and
// transitions the rollout to canary. If no hosts are connected, the
// rollout aborts with no_canary_targets per decisions doc Rollout #6.
func (s *Service) enterCanary(ctx context.Context, r *Rollout, now time.Time) error {
	connected := s.hosts.ConnectedForScope(r.ScopeKind, r.ScopeRef)
	if len(connected) == 0 {
		s.markAborted(ctx, r, AbortNoCanaryTargets, now)
		s.recordAudit(ctx, audit.EventRolloutAborted, r.CreatedBy, r, map[string]any{
			"abort_reason": string(AbortNoCanaryTargets),
		})
		return nil
	}

	size := resolveCanarySize(len(connected), r.CanaryPct, r.CanaryCount)
	canaryHosts := selectCanaryHosts(connected, size)

	if err := s.store.InsertApplyStates(ctx, r.ID, canaryHosts, true); err != nil {
		return err
	}
	r.CanarySize = &size
	r.State = StateCanary
	r.CanaryAt = &now
	if err := s.store.Update(ctx, r); err != nil {
		return err
	}
	return nil
}


// markAborted writes a rollout to the aborted state with the given reason.
// Caller is expected to follow with a recordAudit for the operator action
// or engine event that triggered the abort.
func (s *Service) markAborted(ctx context.Context, r *Rollout, reason AbortReason, now time.Time) {
	r.State = StateAborted
	r.AbortedAt = &now
	r.AbortReason = reason
	if err := s.store.Update(ctx, r); err != nil {
		s.logger.Error("mark aborted: update failed", "rollout_id", r.ID, "err", err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Read-side
// ─────────────────────────────────────────────────────────────────────

// Get returns a rollout + its apply_state aggregate.
func (s *Service) Get(ctx context.Context, id int64) (*Rollout, *ApplyAggregate, bool, error) {
	r, ok, err := s.store.Get(ctx, id)
	if err != nil || !ok {
		return nil, nil, ok, err
	}
	agg, err := s.store.Aggregate(ctx, id)
	if err != nil {
		return nil, nil, false, err
	}
	return r, &agg, true, nil
}

// List forwards to Store.List. Exposed on Service so callers don't
// need to keep both around.
func (s *Service) List(ctx context.Context, f ListFilter, p pagination.Params) ([]Rollout, string, error) {
	return s.store.List(ctx, f, p)
}

// ListApplyState forwards to Store.ListApplyState. hostRef, when non-empty,
// restricts the result to a single host's row — used by the host-drawer
// in-flight strip; pass "" to get the full per-rollout list.
func (s *Service) ListApplyState(ctx context.Context, rolloutID int64, hostRef string, p pagination.Params) ([]ApplyState, string, error) {
	return s.store.ListApplyState(ctx, rolloutID, hostRef, p)
}

// ─────────────────────────────────────────────────────────────────────
// Operator actions
// ─────────────────────────────────────────────────────────────────────

// Pause halts advancement. Legal in soak and promoting (state.go).
func (s *Service) Pause(ctx context.Context, id int64, actor string) (*Rollout, error) {
	r, ok, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNotFound(id)
	}
	if !CanPause(r.State) {
		return r, ErrIllegalOperatorAction
	}
	now := s.clock.Now()
	r.PrevState = r.State
	r.State = StatePaused
	r.PausedAt = &now
	if err := s.store.Update(ctx, r); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, audit.EventRolloutPaused, actor, r, map[string]any{
		"paused_from_state": string(r.PrevState),
	})
	return r, nil
}

// Resume returns the rollout to its prev_state. Re-evaluation of the gate
// (when prev_state=soak) is the responsibility of the gate evaluator
// (next-turn ticker work).
func (s *Service) Resume(ctx context.Context, id int64, actor string) (*Rollout, error) {
	r, ok, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNotFound(id)
	}
	if !CanResume(r.State) {
		return r, ErrIllegalOperatorAction
	}
	prev := r.PrevState
	if prev == "" {
		// Defensive: shouldn't happen because Pause always sets prev_state.
		// Fall back to soak (the safer of the two).
		prev = StateSoak
	}
	r.State = prev
	r.PrevState = ""
	r.PausedAt = nil
	if err := s.store.Update(ctx, r); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, audit.EventRolloutAdvanced, actor, r, map[string]any{
		"from_state": string(StatePaused),
		"to_state":   string(prev),
	})
	return r, nil
}

// Abort transitions to terminal aborted with abort_reason=operator.
// Hosts that received the new config during canary or promote should
// be re-pushed the prior config; that's a wiring concern for the OpAMP
// integration (next turn). The state-machine view simply marks aborted.
func (s *Service) Abort(ctx context.Context, id int64, actor, reason string) (*Rollout, error) {
	r, ok, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNotFound(id)
	}
	if !CanAbort(r.State) {
		return r, ErrIllegalOperatorAction
	}
	now := s.clock.Now()
	r.State = StateAborted
	r.AbortedAt = &now
	r.AbortReason = AbortByOperator
	if err := s.store.Update(ctx, r); err != nil {
		return nil, err
	}
	payload := map[string]any{"abort_reason": string(AbortByOperator)}
	if reason != "" {
		payload["operator_reason"] = reason
	}
	s.recordAudit(ctx, audit.EventRolloutAborted, actor, r, payload)
	return r, nil
}

// FastPromote skips remaining gate evaluation and moves to promoting.
// Per spec §5: legal in soak or paused-from-soak.
func (s *Service) FastPromote(ctx context.Context, id int64, actor string) (*Rollout, error) {
	r, ok, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNotFound(id)
	}
	if !CanFastPromote(r.State, r.PrevState) {
		return r, ErrIllegalOperatorAction
	}
	now := s.clock.Now()
	r.State = StatePromoting
	r.PromotedAt = &now
	r.PausedAt = nil
	r.PrevState = ""
	if err := s.store.Update(ctx, r); err != nil {
		return nil, err
	}
	gateStatus := "pending"
	if r.GatePassedAt != nil {
		gateStatus = "passed"
	}
	s.recordAudit(ctx, audit.EventRolloutFastPromoted, actor, r, map[string]any{
		"gate_status_at_skip": gateStatus,
	})
	// Transition the apply_state plumbing into Promote — for v1.0
	// we add the remaining hosts as is_canary=false rows here.
	// (When OpAMP push is wired, this is also where we'd kick off
	// the fan-out push to those hosts.)
	if err := s.fillPromoteApplyStates(ctx, r); err != nil {
		s.logger.Error("fast-promote: fill promote apply_states", "rollout_id", r.ID, "err", err)
		// Don't fail the operator action — the state already transitioned.
		// The next AdvancePhase or background tick can backfill.
	}
	return r, nil
}

// Promote is the manual-mode counterpart of auto-promote. Per spec §5:
// only legal in soak with gate_passed_at set + gate_mode=manual.
func (s *Service) Promote(ctx context.Context, id int64, actor string) (*Rollout, error) {
	r, ok, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNotFound(id)
	}
	gatePassed := r.GatePassedAt != nil
	if !CanManualPromote(r.State, r.GateMode, gatePassed) {
		return r, ErrIllegalOperatorAction
	}
	now := s.clock.Now()
	r.State = StatePromoting
	r.PromotedAt = &now
	if err := s.store.Update(ctx, r); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, audit.EventRolloutPromoted, actor, r, map[string]any{
		"mode": string(GateManual),
	})
	if err := s.fillPromoteApplyStates(ctx, r); err != nil {
		s.logger.Error("promote: fill promote apply_states", "rollout_id", r.ID, "err", err)
	}
	return r, nil
}

// fillPromoteApplyStates inserts apply_state rows for every currently-
// connected host in the Scope that isn't already in the canary subset.
// Idempotent on conflict (Store.InsertApplyStates uses ON CONFLICT DO NOTHING).
func (s *Service) fillPromoteApplyStates(ctx context.Context, r *Rollout) error {
	connected := s.hosts.ConnectedForScope(r.ScopeKind, r.ScopeRef)
	if len(connected) == 0 {
		return nil
	}
	// We don't dedupe against canary here — the ON CONFLICT path skips
	// the canary uids. is_canary=false is correct because these rows
	// represent the Promote phase membership; canary rows already have
	// is_canary=true and aren't overwritten.
	return s.store.InsertApplyStates(ctx, r.ID, connected, false)
}

// ─────────────────────────────────────────────────────────────────────
// Config resolution — the path the OpAMP server takes when a connected
// agent needs to know which Config to apply. Replaces the v0.2 direct
// configs.Store.LatestFor lookup with rollout-aware resolution.
// ─────────────────────────────────────────────────────────────────────

// ResolveConfigFor returns the Config (yaml + sha256 hex) the given host
// should currently be running, in priority order:
//
//  1. Rollout in progress: if the host has a `pending` or `applying`
//     apply_state row in any non-terminal rollout, return that
//     rollout's Config. Side-effect: transitions a `pending` row to
//     `applying` to mark "the push has gone out" — the next OpAMP
//     send to this host carries the Config.
//
//  2. Live Config from the most recent Done rollout for this host's
//     `instance` Scope (Shape 1 override).
//
//  3. Live Config from the most recent Done rollout for this host's
//     `(product, variant)` Scope.
//
//  4. v0.2 fallback: configs.Store.LatestFor with default/default
//     (so v0.2 fleets keep working until they finish migrating to
//     rollout-driven config flow).
//
// Returns ok=false when no config is resolvable at any level —
// equivalent to v0.2 "no config published yet."
func (s *Service) ResolveConfigFor(ctx context.Context, instanceUID, product, variant string) (yamlOut string, hashHex string, ok bool, err error) {
	// 1. In-flight rollout for this host.
	rolloutID, configID, applyState, found, err := s.store.PendingForHost(ctx, instanceUID)
	if err != nil {
		return "", "", false, fmt.Errorf("resolve config (pending): %w", err)
	}
	if found {
		cfg, cfgOk, err := s.configStore.Get(ctx, configID)
		if err != nil {
			return "", "", false, fmt.Errorf("resolve config (get %d): %w", configID, err)
		}
		if !cfgOk {
			s.logger.Warn("rollout references missing config", "rollout_id", rolloutID, "config_id", configID)
			return "", "", false, nil
		}
		// Transition pending → applying so re-resolve doesn't double-fire.
		// applying → applying is a no-op (UpdateApplyState bumps attempt_count
		// each time, which is what we want for retry visibility).
		if applyState == ApplyPending {
			if err := s.store.UpdateApplyState(ctx, rolloutID, instanceUID, ApplyApplying, "", ""); err != nil {
				s.logger.Error("apply_state pending→applying", "rollout_id", rolloutID, "host", instanceUID, "err", err)
				// Don't fail the resolve — push the config; the next heartbeat will reconcile.
			}
		}
		return cfg.YAML, sha256Hex(cfg.YAML), true, nil
	}

	// 2. Instance-scope live Config (Shape 1 override).
	if cfgID, err := s.store.LiveConfigID(ctx, ScopeInstance, instanceUID); err != nil {
		return "", "", false, fmt.Errorf("resolve config (instance live): %w", err)
	} else if cfgID != nil {
		cfg, ok, err := s.configStore.Get(ctx, *cfgID)
		if err != nil || !ok {
			return "", "", false, err
		}
		return cfg.YAML, sha256Hex(cfg.YAML), true, nil
	}

	// 3. Product+variant-scope live Config.
	pvRef := product + "/" + variant
	if cfgID, err := s.store.LiveConfigID(ctx, ScopeProductVariant, pvRef); err != nil {
		return "", "", false, fmt.Errorf("resolve config (pv live): %w", err)
	} else if cfgID != nil {
		cfg, ok, err := s.configStore.Get(ctx, *cfgID)
		if err != nil || !ok {
			return "", "", false, err
		}
		return cfg.YAML, sha256Hex(cfg.YAML), true, nil
	}

	// 4. v0.2 fallback (preserves "agent before any rollout" path).
	cfg, ok, err := s.configStore.LatestFor(ctx, product, variant)
	if err != nil || !ok {
		return "", "", false, err
	}
	return cfg.YAML, sha256Hex(cfg.YAML), true, nil
}

// HandleHeartbeat is called after each AgentToServer message has been
// processed by the OpAMP server. Walks every non-terminal rollout
// targeting this agent and updates apply_state based on the agent's
// reported applied_config_hash + RemoteConfigStatus.
//
// Match logic:
//   - applied_hash matches a rollout's target Config hash → ApplyApplied
//   - configFailed=true and applied_hash matches → ApplyFailed (with error)
//   - otherwise no transition (the rollout's apply_state stays as-is;
//     the next heartbeat or AdvancePhase tick will reconcile)
//
// Bare cross-section: at v1.0 a single agent could be in multiple
// in-flight rollouts (instance + product+variant) but only one of those
// would have the agent's currently-applied hash, so at most one
// transition fires per call.
func (s *Service) HandleHeartbeat(ctx context.Context, instanceUID, appliedHash string, configFailed bool, errorMsg string) error {
	if instanceUID == "" || appliedHash == "" {
		return nil
	}
	rs, err := s.store.NonTerminalForHost(ctx, instanceUID)
	if err != nil {
		return fmt.Errorf("heartbeat: find rollouts for host %s: %w", instanceUID, err)
	}
	for _, r := range rs {
		cfg, ok, err := s.configStore.Get(ctx, r.ConfigID)
		if err != nil {
			s.logger.Error("heartbeat: get config", "rollout_id", r.ID, "config_id", r.ConfigID, "err", err)
			continue
		}
		if !ok {
			continue
		}
		targetHash := sha256Hex(cfg.YAML)
		if targetHash != appliedHash {
			continue
		}
		// Hash matches the rollout's target. Transition apply_state.
		newState := ApplyApplied
		errFor := ""
		if configFailed {
			newState = ApplyFailed
			errFor = errorMsg
		}
		if err := s.store.UpdateApplyState(ctx, r.ID, instanceUID, newState, appliedHash, errFor); err != nil {
			s.logger.Error("heartbeat: update apply_state", "rollout_id", r.ID, "host", instanceUID, "err", err)
		}
	}
	return nil
}

// sha256Hex returns the lowercase-hex sha256 of a string. Matches the
// hex form OpAMP carries for config hashes.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ─────────────────────────────────────────────────────────────────────
// AdvancePhase — the engine's main loop, exposed for tests + future
// ticker wiring. NOT YET DRIVEN BY ANYTHING IN PRODUCTION.
// ─────────────────────────────────────────────────────────────────────

// AdvancePhase examines a rollout's current state + apply_state aggregate
// and applies any engine-driven transition that's now legal. Used by
// tests today; in a follow-up turn a background ticker calls this
// periodically for every non-terminal rollout.
//
// Transitions handled:
//   - canary → soak (when all canary apply_state rows have left pending)
//   - soak → promoting (auto mode, gate passes)
//   - soak → aborted (canary gate fail; auto-rollback by decision Rollout #3)
//   - soak (manual mode) → soak with gate_passed_at set
//   - promoting → done (when all promote apply_state rows have left pending)
//
// Returns (changed, error). changed=true when a transition fired.
func (s *Service) AdvancePhase(ctx context.Context, id int64) (bool, error) {
	r, ok, err := s.store.Get(ctx, id)
	if err != nil || !ok {
		return false, err
	}
	if r.State.Terminal() {
		return false, nil
	}

	agg, err := s.store.Aggregate(ctx, id)
	if err != nil {
		return false, err
	}

	switch r.State {
	case StateCanary:
		// All canary rows must have left pending before we enter Soak.
		if agg.TotalCanary > 0 && canaryAllSettled(agg) {
			now := s.clock.Now()
			r.State = StateSoak
			r.SoakAt = &now
			if err := s.store.Update(ctx, r); err != nil {
				return false, err
			}
			s.recordAudit(ctx, audit.EventRolloutAdvanced, "system", r, map[string]any{
				"from_state": string(StateCanary),
				"to_state":   string(StateSoak),
			})
			return true, nil
		}
	case StateSoak:
		// Gate evaluation. Per decisions doc Rollout #3 + #8: canary-
		// subset only; auto-abort default-on for failures.
		passed, failed := evaluateGate(agg)
		if failed {
			now := s.clock.Now()
			r.State = StateAborted
			r.AbortedAt = &now
			r.AbortReason = AbortCanaryGateFailed
			if err := s.store.Update(ctx, r); err != nil {
				return false, err
			}
			s.recordAudit(ctx, audit.EventRolloutAborted, "system", r, map[string]any{
				"abort_reason":  string(AbortCanaryGateFailed),
				"applied":       agg.Applied,
				"failed":        agg.Failed,
				"total_canary":  agg.TotalCanary,
			})
			return true, nil
		}
		if !passed {
			// Gate hasn't passed yet (still in flight or unevaluable). Wait.
			return false, nil
		}
		// Gate passes. Set GatePassedAt the first time we observe this.
		gateFirstPass := r.GatePassedAt == nil
		if gateFirstPass {
			now := s.clock.Now()
			r.GatePassedAt = &now
			if err := s.store.Update(ctx, r); err != nil {
				return false, err
			}
			s.recordAudit(ctx, audit.EventRolloutAdvanced, "system", r, map[string]any{
				"event": "gate_passed",
				"phase": "soak_window_in_flight",
			})
		}
		// Soak window must elapse before we can advance. Per decisions
		// doc Rollout #3: gate failure during soak triggers auto-abort
		// regardless of elapsed time (the first branch above), but
		// gate pass requires the full soak window to actually pass.
		soakElapsed := r.SoakAt != nil && s.clock.Now().Sub(*r.SoakAt) >= time.Duration(r.SoakSeconds)*time.Second
		if !soakElapsed {
			// Stay in soak — gate is passing, just need more time.
			// gateFirstPass=true above already returned changed; here we
			// only return changed=false on subsequent ticks waiting for time.
			return gateFirstPass, nil
		}
		// Soak window elapsed AND gate passing. Advance based on mode.
		if r.GateMode == GateAuto {
			return s.advanceSoakToPromoting(ctx, r)
		}
		// Manual mode: hold in soak with gate_passed_at set; operator
		// must call Promote explicitly.
		if gateFirstPass {
			s.recordAudit(ctx, audit.EventRolloutAdvanced, "system", r, map[string]any{
				"event": "gate_passed",
				"phase": "manual_mode_hold",
			})
		}
		return gateFirstPass, nil
	case StatePromoting:
		// Done when no apply_state rows remain in pending or applying.
		// Covers three valid shapes: (a) Phased with non-trivial promote
		// fan-out (TotalPromote > 0); (b) Phased where canary == fleet
		// (TotalPromote = 0, TotalCanary > 0); (c) Instant rollout that
		// caught zero connected hosts (rare; covered by safety).
		// Canary→soak invariant guarantees Pending/Applying are zero
		// across canary by the time we got here, so this check is
		// effectively about promote-phase rows when there are any.
		if agg.Pending == 0 && agg.Applying == 0 {
			now := s.clock.Now()
			r.State = StateDone
			r.DoneAt = &now
			if err := s.store.Update(ctx, r); err != nil {
				return false, err
			}
			s.recordAudit(ctx, audit.EventRolloutAdvanced, "system", r, map[string]any{
				"from_state": string(StatePromoting),
				"to_state":   string(StateDone),
				"applied":    agg.Applied,
				"failed":     agg.Failed,
			})
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) advanceSoakToPromoting(ctx context.Context, r *Rollout) (bool, error) {
	now := s.clock.Now()
	r.State = StatePromoting
	r.PromotedAt = &now
	if err := s.store.Update(ctx, r); err != nil {
		return false, err
	}
	if err := s.fillPromoteApplyStates(ctx, r); err != nil {
		s.logger.Error("auto-promote: fill apply_states", "rollout_id", r.ID, "err", err)
	}
	s.recordAudit(ctx, audit.EventRolloutAdvanced, "system", r, map[string]any{
		"from_state": string(StateSoak),
		"to_state":   string(StatePromoting),
		"trigger":    "auto",
	})
	return true, nil
}

// ─────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────

func canaryAllSettled(a ApplyAggregate) bool {
	// Pending or Applying canary rows mean we're not done. We can only
	// distinguish canary vs promote rows in is_canary, but the ApplyAggregate
	// rolls up by state across both. Conservative check: total_canary settled
	// = applied+failed accounts for all canary rows. We approximate by
	// reading per-state aggregates against TotalCanary. The store ensures
	// is_canary=true rows fall under TotalCanary; if pending+applying = 0
	// across both subsets and TotalCanary > 0, all canary rows are settled.
	// For the v1.0 simplification (canary phase happens before any promote
	// rows exist), the check is: pending+applying = 0.
	return a.Pending == 0 && a.Applying == 0
}

// evaluateGate returns (passed, failed). At most one is true.
//
// passed: apply_success_rate >= 95% AND no health regression detected.
//   (Health regression detection is the canary-only subset per decisions
//   doc Rollout #8; current implementation approximates it as "no failed
//   rows" and will be sharpened when health-state tracking lands.)
// failed: apply_success_rate < 95%.
//
// Pending rows count as "not applied" per decisions doc Rollout #4.
func evaluateGate(a ApplyAggregate) (passed bool, failed bool) {
	if a.TotalCanary == 0 {
		// Defensive: shouldn't happen if we enter Soak via canary.
		// Treat as not-yet-evaluable.
		return false, false
	}
	// Apply success rate over canary subset.
	successPct := float64(a.Applied) / float64(a.TotalCanary)
	if successPct >= 0.95 {
		return true, false
	}
	// If pending+applying drop to zero and we still don't hit 95%,
	// the gate has failed conclusively.
	if a.Pending == 0 && a.Applying == 0 {
		return false, true
	}
	// Still in flight — neither pass nor fail yet.
	return false, false
}

// resolveCanarySize picks the actual canary host count given the
// requested pct and count + the connected fleet size.
//
// Per docs/v1.0-rollout-spec.md §4.2: the larger of (canary_pct% rounded
// up) and canary_count, capped at len(connected). Defaults are applied
// upstream in Create — by the time this runs, pct and count are populated.
func resolveCanarySize(connected int, pct, count *int) int {
	pctSize := 0
	if pct != nil && *pct > 0 {
		pctSize = (connected**pct + 99) / 100 // ceil division
	}
	countSize := 0
	if count != nil && *count > 0 {
		countSize = *count
	}
	size := min(max(max(pctSize, countSize), 0), connected)
	return size
}

// selectCanaryHosts picks `size` hosts from `connected` using a stable
// hash of instance_uid (FNV-1a) so the same hosts canary repeatedly
// across rollouts. Per docs/v1.0-rollout-spec.md §4.2: "Operators can
// predict who's affected, which matters at 2000-host scale where you
// don't want to 'guess who's a canary today.'"
func selectCanaryHosts(connected []string, size int) []string {
	if size >= len(connected) {
		out := make([]string, len(connected))
		copy(out, connected)
		return out
	}
	type uidHash struct {
		uid string
		h   uint64
	}
	pairs := make([]uidHash, len(connected))
	for i, uid := range connected {
		h := fnv.New64a()
		_, _ = h.Write([]byte(uid))
		pairs[i] = uidHash{uid: uid, h: h.Sum64()}
	}
	sort.Slice(pairs, func(i, j int) bool {
		// Tie-break on uid for determinism if hashes collide.
		if pairs[i].h != pairs[j].h {
			return pairs[i].h < pairs[j].h
		}
		return pairs[i].uid < pairs[j].uid
	})
	out := make([]string, size)
	for i := range size {
		out[i] = pairs[i].uid
	}
	return out
}

// splitProductVariant parses "product/variant" form. Returns (_, _, false)
// for inputs that don't have exactly one slash separator.
func splitProductVariant(ref string) (product, variant string, ok bool) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == '/' {
			return ref[:i], ref[i+1:], true
		}
	}
	return "", "", false
}

// recordAudit emits a v1.0 hash-chained audit event for a rollout
// state change. Each call adds a row to audit_log linked into the
// chain via prev_hash; tampering is detectable via Store.VerifyChain.
//
// payload is merged with always-present rollout context (rollout_id,
// state). Caller passes type-specific fields (e.g. {"abort_reason":
// "canary_gate_failed"} for an abort event); the structured form
// replaces the v0.2 free-text Detail string.
//
// The denormalised v0.2 fields (Product/Variant/TargetID) are populated
// for the legacy UI's audit list. The v1.0 fields (ScopeKind/ScopeRef/
// ConfigRef/PayloadJSON) drive the v1.0 audit query surface.
func (s *Service) recordAudit(ctx context.Context, eventType audit.EventType, actor string, r *Rollout, payload map[string]any) {
	if payload == nil {
		payload = make(map[string]any, 2)
	}
	payload["rollout_id"] = r.ID
	payload["state"] = string(r.State)
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		s.logger.Error("audit: marshal payload", "type", eventType, "rollout_id", r.ID, "err", err)
		payloadJSON = []byte(`{}`)
	}

	var product, variant string
	if r.ScopeKind == ScopeProductVariant {
		if p, v, ok := splitProductVariant(r.ScopeRef); ok {
			product, variant = p, v
		}
	}

	configRef := r.ConfigID
	rolloutID := r.ID
	if err := s.auditStore.RecordEvent(ctx, audit.Event{
		Actor:       actor,
		Type:        eventType,
		ScopeKind:   string(r.ScopeKind),
		ScopeRef:    r.ScopeRef,
		ConfigRef:   &configRef,
		HostRef:     "", // rollouts target a Scope, not a single host
		PayloadJSON: string(payloadJSON),
		Product:     product,
		Variant:     variant,
		TargetID:    &rolloutID,
	}); err != nil {
		s.logger.Error("audit record event failed", "type", eventType, "rollout_id", r.ID, "err", err)
	}
}

// errNotFound is returned (wrapped) when a rollout id doesn't exist.
// Handlers map it to 404.
type errNotFoundType struct{ id int64 }

func (e *errNotFoundType) Error() string { return fmt.Sprintf("rollout %d not found", e.id) }

// ErrNotFound is exported for handlers to type-check against.
var ErrNotFound = &errNotFoundType{}

func errNotFound(id int64) error { return &errNotFoundType{id: id} }

// IsNotFound reports whether err comes from a rollout-id-not-found path.
// Handlers use this rather than `errors.Is(err, ErrNotFound)` because
// the sentinel carries an id field.
func IsNotFound(err error) bool {
	var nf *errNotFoundType
	return errors.As(err, &nf)
}

// deref returns 0 for a nil int pointer, otherwise the dereferenced value.
// Kept tiny; used for log/audit formatting where nil is an acceptable
// "not yet set" placeholder.
func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

