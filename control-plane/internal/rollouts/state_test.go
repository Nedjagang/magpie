package rollouts_test

import (
	"testing"

	"github.com/magpie-project/magpie/control-plane/internal/rollouts"
)

// State predicate tests. These guard the legality table from
// docs/v1.0-rollout-spec.md §3 — every regression here is an
// operator-action that becomes silently legal/illegal.

func TestCanPause(t *testing.T) {
	cases := []struct {
		state rollouts.State
		want  bool
	}{
		{rollouts.StateValidating, false},
		{rollouts.StateCanary, false},
		{rollouts.StateSoak, true},
		{rollouts.StatePromoting, true},
		{rollouts.StatePaused, false}, // already paused
		{rollouts.StateDone, false},
		{rollouts.StateAborted, false},
	}
	for _, tc := range cases {
		if got := rollouts.CanPause(tc.state); got != tc.want {
			t.Errorf("CanPause(%q) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

func TestCanResume(t *testing.T) {
	for _, tc := range []struct {
		state rollouts.State
		want  bool
	}{
		{rollouts.StatePaused, true},
		{rollouts.StateValidating, false},
		{rollouts.StateCanary, false},
		{rollouts.StateSoak, false},
		{rollouts.StatePromoting, false},
		{rollouts.StateDone, false},
		{rollouts.StateAborted, false},
	} {
		if got := rollouts.CanResume(tc.state); got != tc.want {
			t.Errorf("CanResume(%q) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

func TestCanAbort(t *testing.T) {
	for _, tc := range []struct {
		state rollouts.State
		want  bool
	}{
		{rollouts.StateValidating, true},
		{rollouts.StateCanary, true},
		{rollouts.StateSoak, true},
		{rollouts.StatePromoting, true},
		{rollouts.StatePaused, true},
		{rollouts.StateDone, false},
		{rollouts.StateAborted, false},
	} {
		if got := rollouts.CanAbort(tc.state); got != tc.want {
			t.Errorf("CanAbort(%q) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

func TestCanFastPromote(t *testing.T) {
	cases := []struct {
		current rollouts.State
		prev    rollouts.State
		want    bool
	}{
		{rollouts.StateSoak, "", true},                               // soak → fast-promote always legal
		{rollouts.StatePaused, rollouts.StateSoak, true},             // paused-from-soak → legal
		{rollouts.StatePaused, rollouts.StatePromoting, false},       // paused-from-promoting → not legal (already past gate)
		{rollouts.StateCanary, "", false},                            // canary → not legal (gate hasn't been evaluated)
		{rollouts.StateValidating, "", false},
		{rollouts.StatePromoting, "", false},                         // already promoting
		{rollouts.StateDone, "", false},
		{rollouts.StateAborted, "", false},
	}
	for _, tc := range cases {
		if got := rollouts.CanFastPromote(tc.current, tc.prev); got != tc.want {
			t.Errorf("CanFastPromote(%q, prev=%q) = %v, want %v",
				tc.current, tc.prev, got, tc.want)
		}
	}
}

func TestCanManualPromote(t *testing.T) {
	cases := []struct {
		current     rollouts.State
		mode        rollouts.GateMode
		gatePassed  bool
		want        bool
		description string
	}{
		{rollouts.StateSoak, rollouts.GateManual, true, true, "soak + manual + gate passed → legal"},
		{rollouts.StateSoak, rollouts.GateManual, false, false, "soak + manual + gate not passed → not legal"},
		{rollouts.StateSoak, rollouts.GateAuto, true, false, "auto mode → operator promote not used"},
		{rollouts.StateCanary, rollouts.GateManual, true, false, "canary state → not legal (haven't reached soak)"},
		{rollouts.StatePromoting, rollouts.GateManual, true, false, "already promoting → not legal"},
	}
	for _, tc := range cases {
		got := rollouts.CanManualPromote(tc.current, tc.mode, tc.gatePassed)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.description, got, tc.want)
		}
	}
}

func TestStateTerminalPredicate(t *testing.T) {
	for _, tc := range []struct {
		state    rollouts.State
		terminal bool
	}{
		{rollouts.StateValidating, false},
		{rollouts.StateCanary, false},
		{rollouts.StateSoak, false},
		{rollouts.StatePromoting, false},
		{rollouts.StatePaused, false},
		{rollouts.StateDone, true},
		{rollouts.StateAborted, true},
	} {
		if got := tc.state.Terminal(); got != tc.terminal {
			t.Errorf("State(%q).Terminal() = %v, want %v", tc.state, got, tc.terminal)
		}
	}
}
