// Package rollouts implements the v1.0 Rollout primitive — the unit of
// intentional state change in Magpie.
//
// See docs/v1.0-rollout-spec.md for the full spec, docs/v1.0-decisions.md
// for the working assumptions committed on 2026-05-09.
//
// Package shape:
//   - types.go   — data types + enums + small predicates
//   - state.go   — state-machine validation (which operator actions are
//                  legal from which states)
//   - store.go   — *db.Conn-backed persistence over the rollouts +
//                  apply_state tables (migration 00008)
//   - service.go — orchestration: Create flow, operator actions, audit
//                  emission. Calls state.go for legality and store.go
//                  for persistence.
//
// Deliberately not in this package (yet):
//   - OpAMP push integration. When a rollout enters Canary or Promote,
//     production needs to actually push the new Config to those hosts
//     and track ApplyState transitions from agent heartbeats. The state
//     machine here is push-agnostic; integration with internal/opamp
//     lands in a follow-up.
//   - Background gate-evaluator ticker. The Soak phase needs a periodic
//     evaluator to advance to Promote (auto mode) or trip auto-abort on
//     gate failure. The state machine accepts those transitions; a
//     ticker that drives them lands alongside OpAMP push.
package rollouts

import "time"

// ScopeKind names the addressable target shape for a rollout.
// Reserved (not implemented in v1.0): "labels".
type ScopeKind string

const (
	// ScopeProductVariant — most rollouts. scope_ref is "product/variant".
	ScopeProductVariant ScopeKind = "product_variant"

	// ScopeInstance — Shape 1 per-host override (decisions doc Rollout #9).
	// scope_ref is the agent instance_uid.
	ScopeInstance ScopeKind = "instance"
)

// Valid reports whether this ScopeKind is a known v1.0 value.
func (s ScopeKind) Valid() bool {
	return s == ScopeProductVariant || s == ScopeInstance
}

// State is the rollout state machine.
type State string

const (
	StateValidating State = "validating"
	StateCanary     State = "canary"
	StateSoak       State = "soak"
	StatePromoting  State = "promoting"
	StatePaused     State = "paused"
	StateDone       State = "done"
	StateAborted    State = "aborted"
)

// Terminal reports whether this state is a final state.
func (s State) Terminal() bool { return s == StateDone || s == StateAborted }

// Kind distinguishes Phased (default, validate→canary→soak→promote→done)
// from Instant (validate→promoting→done, used for emergencies).
type Kind string

const (
	KindPhased  Kind = "phased"
	KindInstant Kind = "instant"
)

func (k Kind) Valid() bool { return k == KindPhased || k == KindInstant }

// GateMode controls whether the canary→promote transition is automatic
// (after gate passes) or requires explicit operator action.
type GateMode string

const (
	GateAuto   GateMode = "auto"
	GateManual GateMode = "manual"
)

func (g GateMode) Valid() bool { return g == GateAuto || g == GateManual }

// AbortReason captures why a rollout reached the aborted state.
// Stored as TEXT in the rollouts table so v1.x can extend the set
// without a schema migration.
type AbortReason string

const (
	AbortByOperator             AbortReason = "operator"
	AbortCanaryGateFailed       AbortReason = "canary_gate_failed"
	AbortValidateFailed         AbortReason = "validate_failed"
	AbortValidateTimeout        AbortReason = "validate_timeout"
	AbortSemanticValidateFailed AbortReason = "semantic_validate_failed"
	AbortNoCanaryTargets        AbortReason = "no_canary_targets"
)

// ApplyStateValue is the per-host state machine for config delivery.
type ApplyStateValue string

const (
	ApplyPending  ApplyStateValue = "pending"
	ApplyApplying ApplyStateValue = "applying"
	ApplyApplied  ApplyStateValue = "applied"
	ApplyFailed   ApplyStateValue = "failed"
)

// Rollout is the persisted shape of a rollouts row, JSON-serialisable
// for the API. Pointer-typed timestamps mean "not yet reached" — matches
// the schema's NULLable phase-entry columns.
type Rollout struct {
	ID            int64       `json:"id"`
	ScopeKind     ScopeKind   `json:"scope_kind"`
	ScopeRef      string      `json:"scope_ref"`
	ConfigID      int64       `json:"config_id"`
	PriorConfigID *int64      `json:"prior_config_id,omitempty"`
	Kind          Kind        `json:"rollout_kind"`
	State         State       `json:"state"`
	PrevState     State       `json:"prev_state,omitempty"`
	CanaryPct     *int        `json:"canary_pct,omitempty"`
	CanaryCount   *int        `json:"canary_count,omitempty"`
	CanarySize    *int        `json:"canary_size,omitempty"`
	SoakSeconds   int         `json:"soak_seconds"`
	GateMode      GateMode    `json:"gate_mode"`
	GatePassedAt  *time.Time  `json:"gate_passed_at,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	CreatedBy     string      `json:"created_by"`
	ValidatedAt   *time.Time  `json:"validated_at,omitempty"`
	CanaryAt      *time.Time  `json:"canary_at,omitempty"`
	SoakAt        *time.Time  `json:"soak_at,omitempty"`
	PromotedAt    *time.Time  `json:"promoted_at,omitempty"`
	DoneAt        *time.Time  `json:"done_at,omitempty"`
	PausedAt      *time.Time  `json:"paused_at,omitempty"`
	AbortedAt     *time.Time  `json:"aborted_at,omitempty"`
	AbortReason   AbortReason `json:"abort_reason,omitempty"`
}

// ApplyState is the per-host record for a rollout. There's at most one
// row per (rollout_id, instance_uid).
type ApplyState struct {
	RolloutID    int64           `json:"rollout_id"`
	InstanceUID  string          `json:"instance_uid"`
	State        ApplyStateValue `json:"state"`
	IsCanary     bool            `json:"is_canary"`
	AttemptCount int             `json:"attempt_count"`
	AppliedHash  string          `json:"applied_hash,omitempty"`
	LastError    string          `json:"last_error,omitempty"`
	PushedAt     *time.Time      `json:"pushed_at,omitempty"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// ApplyAggregate is the rollup of apply_state rows for a rollout, used
// by the publish dialog's live-progress view and the Rollouts UI section
// (per docs/v1.0-publish-dialog-spec.md §10).
type ApplyAggregate struct {
	Pending      int `json:"pending"`
	Applying     int `json:"applying"`
	Applied      int `json:"applied"`
	Failed       int `json:"failed"`
	TotalCanary  int `json:"total_canary"`
	TotalPromote int `json:"total_promote"`
}
