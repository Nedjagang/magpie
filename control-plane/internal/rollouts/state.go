package rollouts

import "errors"

// State machine validation. These predicates encode the legality table
// from docs/v1.0-rollout-spec.md §3 ("Transitions") so handlers and the
// service layer can reject illegal operator actions with 409 instead of
// silently corrupting state.
//
// Each predicate corresponds to one operator-initiated event. Engine-
// initiated transitions (validate pass/fail, all_delivered, gate
// auto-pass/fail) live inside service.go because they always come with
// side effects (creating apply_state rows, emitting audit events) that
// belong with orchestration.

// ErrIllegalOperatorAction is returned when an operator-initiated action
// is rejected because the rollout's current state doesn't allow it.
// Handlers map this to 409 Conflict.
var ErrIllegalOperatorAction = errors.New("rollout: action not legal in current state")

// CanPause reports whether an operator can pause the rollout right now.
// Pause is allowed in soak and promoting; not in validating, canary,
// paused (already paused), done, or aborted. Per spec §3:
//
//	"Pause halts advancement only. ... Pause is allowed in soak and
//	 promoting. Not allowed in validating (Validate is fast and atomic)
//	 or canary (Canary is "deliver to N hosts" which is a fan-out the
//	 system runs to completion before evaluating; pausing mid-fan-out
//	 is a complexity v1.0 doesn't need)."
func CanPause(s State) bool {
	return s == StateSoak || s == StatePromoting
}

// CanResume reports whether an operator can resume the rollout.
// Only legal from paused; the caller of Resume must read prev_state
// from the rollout to know which state to return to.
func CanResume(s State) bool {
	return s == StatePaused
}

// CanAbort reports whether an operator can abort the rollout.
// Allowed in any non-terminal state.
func CanAbort(s State) bool {
	return !s.Terminal()
}

// CanFastPromote reports whether an operator can fast-promote.
// Per spec §5: "Where: soak, or paused (when prev_state=soak). Disabled
// during canary and validating (gates haven't been evaluated yet —
// nothing to skip)."
//
// prev is only consulted when current state is paused.
func CanFastPromote(current State, prev State) bool {
	if current == StateSoak {
		return true
	}
	if current == StatePaused && prev == StateSoak {
		return true
	}
	return false
}

// CanManualPromote reports whether an operator-initiated Promote is legal.
// This is the manual-mode counterpart of automatic promote — only legal
// when:
//   - current state is soak (where the gate evaluator parks rollouts in
//     manual mode after gate passes)
//   - gate_mode is manual
//   - the gate has actually passed (gate_passed_at is non-nil)
//
// Operators in auto-mode never need this — auto-promote fires from the
// gate evaluator side, not from an explicit operator action.
func CanManualPromote(current State, gm GateMode, gatePassed bool) bool {
	return current == StateSoak && gm == GateManual && gatePassed
}
