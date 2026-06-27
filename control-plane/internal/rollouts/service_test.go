package rollouts_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/magpie-project/magpie/control-plane/internal/audit"
	"github.com/magpie-project/magpie/control-plane/internal/configs"
	"github.com/magpie-project/magpie/control-plane/internal/db"
	"github.com/magpie-project/magpie/control-plane/internal/pagination"
	"github.com/magpie-project/magpie/control-plane/internal/rollouts"
)

// testClock is a controllable Clock for tests that exercise the
// soak-window timing in AdvancePhase. Advance() moves the clock
// forward; Now() returns the current value.
type testClock struct{ now time.Time }

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time          { return c.now }
func (c *testClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// fakeResolver returns a fixed list of host uids for any Scope it's
// asked about — sufficient for the v1.0 state-machine + create-flow
// tests that don't yet care about Scope-keyed filtering.
type fakeResolver struct{ hosts []string }

func (f *fakeResolver) ConnectedForScope(_ rollouts.ScopeKind, _ string) []string {
	out := make([]string, len(f.hosts))
	copy(out, f.hosts)
	return out
}

// validYAML is a minimal collector config that passes structural
// validation (configs.Validate). Has one pipeline with one receiver
// and one exporter — the minimum for a useful collector.
const validYAML = `receivers:
  otlp:
    protocols:
      grpc: {}
exporters:
  otlp:
    endpoint: "test:4317"
service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [otlp]
`

// invalidYAML lacks an exporter list in its pipeline. configs.Validate
// fails it with "pipeline X has no exporters".
const invalidYAML = `receivers:
  otlp:
    protocols:
      grpc: {}
exporters:
  otlp:
    endpoint: "test:4317"
service:
  pipelines:
    traces:
      receivers: [otlp]
`

func newTestService(t *testing.T, hosts ...string) (*rollouts.Service, *rollouts.Store, *db.Conn) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	cs := configs.NewStore(conn)
	as := audit.NewStore(conn)
	rs := rollouts.NewStore(conn)
	resolver := &fakeResolver{hosts: hosts}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	svc := rollouts.NewService(rs, cs, resolver, configs.Policy{}, as, logger)
	return svc, rs, conn
}

// ─────────────────────────────────────────────────────────────────────
// Create flow
// ─────────────────────────────────────────────────────────────────────

func TestCreatePhasedHappyPath(t *testing.T) {
	svc, _, _ := newTestService(t, "host-1", "host-2", "host-3", "host-4")
	ctx := context.Background()

	r, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind:  rollouts.ScopeProductVariant,
		ScopeRef:   "ship/linux",
		ConfigName: "ship-linux-v1",
		ConfigYAML: validYAML,
		Kind:       rollouts.KindPhased,
	}, "test@aptean")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if r.State != rollouts.StateCanary {
		t.Errorf("State = %q, want canary", r.State)
	}
	if r.ValidatedAt == nil {
		t.Errorf("ValidatedAt should be set after validate pass")
	}
	if r.CanaryAt == nil {
		t.Errorf("CanaryAt should be set after entering canary")
	}
	if r.CanarySize == nil || *r.CanarySize == 0 {
		t.Errorf("CanarySize should be > 0; got %v", r.CanarySize)
	}
	// At 4 connected hosts, defaults (5%, N=10) → canary = max(ceil(0.05*4), 10) = 10, capped at 4 = 4.
	if *r.CanarySize != 4 {
		t.Errorf("CanarySize = %d, want 4 (capped at connected fleet)", *r.CanarySize)
	}
}

func TestCreatePhasedNoHostsAborts(t *testing.T) {
	svc, _, _ := newTestService(t /* no hosts */)
	ctx := context.Background()

	r, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind:  rollouts.ScopeProductVariant,
		ScopeRef:   "ship/linux",
		ConfigName: "ship-linux-v1",
		ConfigYAML: validYAML,
		Kind:       rollouts.KindPhased,
	}, "test@aptean")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if r.State != rollouts.StateAborted {
		t.Errorf("State = %q, want aborted", r.State)
	}
	if r.AbortReason != rollouts.AbortNoCanaryTargets {
		t.Errorf("AbortReason = %q, want no_canary_targets", r.AbortReason)
	}
}

func TestCreatePhasedInvalidYAMLAborts(t *testing.T) {
	svc, _, _ := newTestService(t, "host-1")
	ctx := context.Background()

	r, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind:  rollouts.ScopeProductVariant,
		ScopeRef:   "ship/linux",
		ConfigName: "ship-linux-v1",
		ConfigYAML: invalidYAML,
		Kind:       rollouts.KindPhased,
	}, "test@aptean")

	var ev *rollouts.ErrValidate
	if !errors.As(err, &ev) {
		t.Fatalf("err = %v, want *ErrValidate", err)
	}
	if r == nil {
		t.Fatalf("rollout should still be returned (in aborted state) so the audit chain is forensic")
	}
	if r.State != rollouts.StateAborted {
		t.Errorf("State = %q, want aborted", r.State)
	}
	if r.AbortReason != rollouts.AbortValidateFailed {
		t.Errorf("AbortReason = %q, want validate_failed", r.AbortReason)
	}
}

func TestCreateInFlightConflict(t *testing.T) {
	svc, _, _ := newTestService(t, "host-1")
	ctx := context.Background()

	first, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind:  rollouts.ScopeProductVariant,
		ScopeRef:   "ship/linux",
		ConfigName: "v1",
		ConfigYAML: validYAML,
		Kind:       rollouts.KindPhased,
	}, "alice")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	_, err = svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind:  rollouts.ScopeProductVariant,
		ScopeRef:   "ship/linux",
		ConfigName: "v2",
		ConfigYAML: validYAML,
		Kind:       rollouts.KindPhased,
	}, "bob")

	var ein *rollouts.ErrInFlight
	if !errors.As(err, &ein) {
		t.Fatalf("err = %v, want *ErrInFlight", err)
	}
	if ein.ExistingID != first.ID {
		t.Errorf("ErrInFlight.ExistingID = %d, want %d", ein.ExistingID, first.ID)
	}
}

func TestCreateInstantSkipsCanary(t *testing.T) {
	svc, _, _ := newTestService(t, "host-1", "host-2", "host-3")
	ctx := context.Background()

	r, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind:  rollouts.ScopeProductVariant,
		ScopeRef:   "ship/linux",
		ConfigName: "v1",
		ConfigYAML: validYAML,
		Kind:       rollouts.KindInstant,
	}, "alice")
	if err != nil {
		t.Fatalf("Create instant: %v", err)
	}

	if r.State != rollouts.StateDone {
		t.Errorf("State = %q, want done (instant goes straight to done)", r.State)
	}
	if r.DoneAt == nil {
		t.Errorf("DoneAt should be set for instant rollout")
	}
	if r.PromotedAt == nil {
		t.Errorf("PromotedAt should be set")
	}
	if r.CanarySize != nil {
		t.Errorf("CanarySize should be nil for Instant rollouts; got %v", *r.CanarySize)
	}
}

func TestCreateInstanceScope(t *testing.T) {
	svc, _, _ := newTestService(t, "host-special")
	ctx := context.Background()

	r, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind:  rollouts.ScopeInstance,
		ScopeRef:   "host-special",
		ConfigName: "host-special-override-v1",
		ConfigYAML: validYAML,
		Kind:       rollouts.KindPhased,
	}, "alice")
	if err != nil {
		t.Fatalf("Create instance-scope: %v", err)
	}

	if r.ScopeKind != rollouts.ScopeInstance {
		t.Errorf("ScopeKind = %q, want instance", r.ScopeKind)
	}
	if r.ScopeRef != "host-special" {
		t.Errorf("ScopeRef = %q, want host-special", r.ScopeRef)
	}
	// Instance scope with the host connected → canary = the host itself.
	if r.State != rollouts.StateCanary {
		t.Errorf("State = %q, want canary", r.State)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Operator actions
// ─────────────────────────────────────────────────────────────────────

func TestPauseIllegalInCanary(t *testing.T) {
	svc, _, _ := newTestService(t, "host-1")
	ctx := context.Background()

	r, _ := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")

	_, err := svc.Pause(ctx, r.ID, "alice")
	if !errors.Is(err, rollouts.ErrIllegalOperatorAction) {
		t.Errorf("Pause from canary err = %v, want ErrIllegalOperatorAction", err)
	}
}

func TestPauseFromSoak(t *testing.T) {
	svc, store, _ := newTestService(t, "host-1")
	ctx := context.Background()

	r, _ := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")

	// Settle the canary apply_state row, then advance.
	if err := store.UpdateApplyState(ctx, r.ID, "host-1", rollouts.ApplyApplied, "abc", ""); err != nil {
		t.Fatalf("UpdateApplyState: %v", err)
	}
	if changed, err := svc.AdvancePhase(ctx, r.ID); err != nil || !changed {
		t.Fatalf("AdvancePhase canary→soak: changed=%v err=%v", changed, err)
	}

	paused, err := svc.Pause(ctx, r.ID, "alice")
	if err != nil {
		t.Fatalf("Pause from soak: %v", err)
	}
	if paused.State != rollouts.StatePaused {
		t.Errorf("State = %q, want paused", paused.State)
	}
	if paused.PrevState != rollouts.StateSoak {
		t.Errorf("PrevState = %q, want soak", paused.PrevState)
	}
}

func TestResumeReturnsToPrevState(t *testing.T) {
	svc, store, _ := newTestService(t, "host-1")
	ctx := context.Background()

	r, _ := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")
	store.UpdateApplyState(ctx, r.ID, "host-1", rollouts.ApplyApplied, "abc", "")
	svc.AdvancePhase(ctx, r.ID) // → soak

	if _, err := svc.Pause(ctx, r.ID, "alice"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	resumed, err := svc.Resume(ctx, r.ID, "alice")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.State != rollouts.StateSoak {
		t.Errorf("State after resume = %q, want soak", resumed.State)
	}
	if resumed.PausedAt != nil {
		t.Errorf("PausedAt should be cleared after Resume")
	}
}

func TestAbortFromCanary(t *testing.T) {
	svc, _, _ := newTestService(t, "host-1")
	ctx := context.Background()

	r, _ := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")

	aborted, err := svc.Abort(ctx, r.ID, "alice", "manual investigation")
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if aborted.State != rollouts.StateAborted {
		t.Errorf("State = %q, want aborted", aborted.State)
	}
	if aborted.AbortReason != rollouts.AbortByOperator {
		t.Errorf("AbortReason = %q, want operator", aborted.AbortReason)
	}
}

func TestFastPromoteFromCanaryIllegal(t *testing.T) {
	svc, _, _ := newTestService(t, "host-1")
	ctx := context.Background()

	r, _ := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")

	_, err := svc.FastPromote(ctx, r.ID, "alice")
	if !errors.Is(err, rollouts.ErrIllegalOperatorAction) {
		t.Errorf("FastPromote from canary err = %v, want ErrIllegalOperatorAction", err)
	}
}

func TestFastPromoteFromSoak(t *testing.T) {
	svc, store, _ := newTestService(t, "host-1", "host-2", "host-3")
	ctx := context.Background()

	r, _ := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")

	canary := canaryHosts(t, ctx, store, r.ID)
	for _, uid := range canary {
		store.UpdateApplyState(ctx, r.ID, uid, rollouts.ApplyApplied, "abc", "")
	}
	svc.AdvancePhase(ctx, r.ID) // → soak

	promoted, err := svc.FastPromote(ctx, r.ID, "alice")
	if err != nil {
		t.Fatalf("FastPromote from soak: %v", err)
	}
	if promoted.State != rollouts.StatePromoting {
		t.Errorf("State = %q, want promoting", promoted.State)
	}
	if promoted.PromotedAt == nil {
		t.Errorf("PromotedAt should be set")
	}
}

// ─────────────────────────────────────────────────────────────────────
// AdvancePhase — engine-driven transitions
// ─────────────────────────────────────────────────────────────────────

func TestAdvanceCanaryToSoak(t *testing.T) {
	svc, store, _ := newTestService(t, "host-1", "host-2")
	ctx := context.Background()

	r, _ := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")

	// Before settling: AdvancePhase no-op.
	changed, err := svc.AdvancePhase(ctx, r.ID)
	if err != nil {
		t.Fatalf("AdvancePhase (before settle): %v", err)
	}
	if changed {
		t.Errorf("changed=true before canary apply_state settled")
	}

	// Settle every canary row.
	for _, uid := range canaryHosts(t, ctx, store, r.ID) {
		if err := store.UpdateApplyState(ctx, r.ID, uid, rollouts.ApplyApplied, "abc", ""); err != nil {
			t.Fatalf("UpdateApplyState: %v", err)
		}
	}

	changed, err = svc.AdvancePhase(ctx, r.ID)
	if err != nil || !changed {
		t.Fatalf("AdvancePhase canary→soak: changed=%v err=%v", changed, err)
	}

	r2, _, _, _ := svc.Get(ctx, r.ID)
	if r2.State != rollouts.StateSoak {
		t.Errorf("State after advance = %q, want soak", r2.State)
	}
	if r2.SoakAt == nil {
		t.Errorf("SoakAt should be set after entering soak")
	}
}

func TestAdvanceSoakAutoToPromoting(t *testing.T) {
	svc, store, _ := newTestService(t, "host-1")
	clock := newTestClock()
	svc.SetClock(clock)
	ctx := context.Background()

	r, _ := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
		// GateMode unspecified → default Auto.
	}, "alice")

	for _, uid := range canaryHosts(t, ctx, store, r.ID) {
		store.UpdateApplyState(ctx, r.ID, uid, rollouts.ApplyApplied, "abc", "")
	}
	svc.AdvancePhase(ctx, r.ID) // → soak

	// First tick: gate passes, gate_passed_at set, but soak window
	// hasn't elapsed yet. Stays in soak.
	changed, err := svc.AdvancePhase(ctx, r.ID)
	if err != nil {
		t.Fatalf("AdvancePhase soak (gate pass): %v", err)
	}
	if !changed {
		t.Errorf("changed=false on first gate-pass tick; expected true (gate_passed_at flipped)")
	}
	r1, _, _, _ := svc.Get(ctx, r.ID)
	if r1.State != rollouts.StateSoak {
		t.Errorf("State after first tick = %q, want soak (still elapsing)", r1.State)
	}
	if r1.GatePassedAt == nil {
		t.Errorf("GatePassedAt should be set after first gate-pass tick")
	}

	// Advance the clock past the soak window (default 300s). Now AdvancePhase
	// should transition soak→promoting.
	clock.Advance(301 * time.Second)
	changed, err = svc.AdvancePhase(ctx, r.ID)
	if err != nil || !changed {
		t.Fatalf("AdvancePhase soak→promoting after elapse: changed=%v err=%v", changed, err)
	}

	r2, _, _, _ := svc.Get(ctx, r.ID)
	if r2.State != rollouts.StatePromoting {
		t.Errorf("State after soak elapse = %q, want promoting", r2.State)
	}
	if r2.PromotedAt == nil {
		t.Errorf("PromotedAt should be set")
	}
}

func TestAdvanceSoakManualHoldsAtGatePassed(t *testing.T) {
	svc, store, _ := newTestService(t, "host-1")
	clock := newTestClock()
	svc.SetClock(clock)
	ctx := context.Background()

	r, _ := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
		GateMode: rollouts.GateManual,
	}, "alice")

	for _, uid := range canaryHosts(t, ctx, store, r.ID) {
		store.UpdateApplyState(ctx, r.ID, uid, rollouts.ApplyApplied, "abc", "")
	}
	svc.AdvancePhase(ctx, r.ID) // → soak
	svc.AdvancePhase(ctx, r.ID) // gate passes; gate_passed_at set; manual mode holds.

	// Even after the soak window elapses, manual mode keeps the rollout
	// in soak — the operator must call Promote explicitly.
	clock.Advance(301 * time.Second)
	svc.AdvancePhase(ctx, r.ID)

	r2, _, _, _ := svc.Get(ctx, r.ID)
	if r2.State != rollouts.StateSoak {
		t.Errorf("State = %q, want soak (manual hold should persist past soak elapse)", r2.State)
	}
	if r2.GatePassedAt == nil {
		t.Errorf("GatePassedAt should be set in manual hold")
	}

	// Operator manual Promote should now succeed.
	promoted, err := svc.Promote(ctx, r.ID, "alice")
	if err != nil {
		t.Fatalf("Promote (manual): %v", err)
	}
	if promoted.State != rollouts.StatePromoting {
		t.Errorf("State after manual Promote = %q, want promoting", promoted.State)
	}
}

func TestAdvanceSoakFailsGateAborts(t *testing.T) {
	svc, store, _ := newTestService(t, "host-1", "host-2")
	ctx := context.Background()

	r, _ := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")

	// Mark every canary row as failed → gate fails.
	for _, uid := range canaryHosts(t, ctx, store, r.ID) {
		store.UpdateApplyState(ctx, r.ID, uid, rollouts.ApplyFailed, "", "boom")
	}
	svc.AdvancePhase(ctx, r.ID) // canary→soak (no pending left)
	svc.AdvancePhase(ctx, r.ID) // gate evaluates, fails, auto-abort

	r2, _, _, _ := svc.Get(ctx, r.ID)
	if r2.State != rollouts.StateAborted {
		t.Errorf("State after gate fail = %q, want aborted", r2.State)
	}
	if r2.AbortReason != rollouts.AbortCanaryGateFailed {
		t.Errorf("AbortReason = %q, want canary_gate_failed", r2.AbortReason)
	}
}

func TestAdvancePromotingToDone(t *testing.T) {
	svc, store, _ := newTestService(t, "host-1", "host-2", "host-3")
	clock := newTestClock()
	svc.SetClock(clock)
	ctx := context.Background()

	r, _ := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")

	for _, uid := range canaryHosts(t, ctx, store, r.ID) {
		store.UpdateApplyState(ctx, r.ID, uid, rollouts.ApplyApplied, "abc", "")
	}
	svc.AdvancePhase(ctx, r.ID) // → soak
	svc.AdvancePhase(ctx, r.ID) // first soak tick: gate passes, gate_passed_at set, soak still elapsing
	clock.Advance(301 * time.Second)
	svc.AdvancePhase(ctx, r.ID) // → promoting (soak elapsed, gate passes, auto)

	// Settle every promote row.
	all, _, _ := store.ListApplyState(ctx, r.ID, "", paginationAll())
	for _, as := range all {
		if as.State == rollouts.ApplyPending {
			store.UpdateApplyState(ctx, r.ID, as.InstanceUID, rollouts.ApplyApplied, "abc", "")
		}
	}

	changed, err := svc.AdvancePhase(ctx, r.ID) // → done
	if err != nil || !changed {
		t.Fatalf("AdvancePhase promoting→done: changed=%v err=%v", changed, err)
	}

	r2, agg, _, _ := svc.Get(ctx, r.ID)
	if r2.State != rollouts.StateDone {
		t.Errorf("State = %q, want done", r2.State)
	}
	if r2.DoneAt == nil {
		t.Errorf("DoneAt should be set")
	}
	if agg.Pending+agg.Applying != 0 {
		t.Errorf("Aggregate not fully settled: %+v", agg)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Config resolution + heartbeat handling (the OpAMP-side integration)
// ─────────────────────────────────────────────────────────────────────

func TestResolveConfigForReturnsRolloutTargetAndAdvancesApplyState(t *testing.T) {
	svc, store, _ := newTestService(t, "host-1")
	ctx := context.Background()

	r, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Resolve for the canary host. Should return the rollout's Config
	// (validYAML) and side-effect-transition pending → applying.
	yaml, hash, ok, err := svc.ResolveConfigFor(ctx, "host-1", "ship", "linux")
	if err != nil {
		t.Fatalf("ResolveConfigFor: %v", err)
	}
	if !ok {
		t.Fatalf("ResolveConfigFor returned ok=false; expected the rollout's Config")
	}
	if yaml != validYAML {
		t.Errorf("YAML mismatch: rollout target should be returned")
	}
	if hash == "" {
		t.Errorf("hash should be non-empty")
	}

	// Verify apply_state advanced to applying.
	rows, _, _ := store.ListApplyState(ctx, r.ID, "", paginationAll())
	for _, as := range rows {
		if as.InstanceUID == "host-1" && as.State != rollouts.ApplyApplying {
			t.Errorf("apply_state for canary host = %q, want applying (after Resolve side-effect)", as.State)
		}
	}
}

func TestResolveConfigForFallsBackToV02PathWhenNoRollouts(t *testing.T) {
	svc, _, conn := newTestService(t)
	ctx := context.Background()

	// Seed a v0.2-style config (no rollout yet) by calling configs.Store directly.
	cs := configs.NewStore(conn)
	if _, err := cs.Create(ctx, "ship", "linux", "v0", validYAML); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	yaml, _, ok, err := svc.ResolveConfigFor(ctx, "host-1", "ship", "linux")
	if err != nil {
		t.Fatalf("ResolveConfigFor: %v", err)
	}
	if !ok {
		t.Fatalf("ResolveConfigFor returned ok=false; expected the v0.2 fallback to succeed")
	}
	if yaml != validYAML {
		t.Errorf("YAML mismatch: v0.2 fallback should return the seeded config")
	}
}

func TestHandleHeartbeatTransitionsApplyStateOnMatch(t *testing.T) {
	svc, store, _ := newTestService(t, "host-1")
	ctx := context.Background()

	r, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Compute target hash by calling ResolveConfigFor — same code path
	// the OpAMP server takes. ResolveConfigFor side-effects apply_state
	// to applying.
	_, targetHash, _, _ := svc.ResolveConfigFor(ctx, "host-1", "ship", "linux")

	// Simulate the agent reporting back with the matching applied hash.
	if err := svc.HandleHeartbeat(ctx, "host-1", targetHash, false, ""); err != nil {
		t.Fatalf("HandleHeartbeat: %v", err)
	}

	rows, _, _ := store.ListApplyState(ctx, r.ID, "", paginationAll())
	for _, as := range rows {
		if as.InstanceUID == "host-1" && as.State != rollouts.ApplyApplied {
			t.Errorf("apply_state after matching heartbeat = %q, want applied", as.State)
		}
	}
}

func TestHandleHeartbeatMarksFailedOnConfigFailed(t *testing.T) {
	svc, store, _ := newTestService(t, "host-1")
	ctx := context.Background()

	r, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, targetHash, _, _ := svc.ResolveConfigFor(ctx, "host-1", "ship", "linux")

	// Agent reports the right hash but with configFailed=true.
	if err := svc.HandleHeartbeat(ctx, "host-1", targetHash, true, "processor not configured"); err != nil {
		t.Fatalf("HandleHeartbeat: %v", err)
	}

	rows, _, _ := store.ListApplyState(ctx, r.ID, "", paginationAll())
	for _, as := range rows {
		if as.InstanceUID == "host-1" {
			if as.State != rollouts.ApplyFailed {
				t.Errorf("apply_state = %q, want failed (configFailed=true)", as.State)
			}
			if as.LastError != "processor not configured" {
				t.Errorf("LastError = %q, want %q", as.LastError, "processor not configured")
			}
		}
	}
}

func TestHandleHeartbeatNoOpOnHashMismatch(t *testing.T) {
	// Hash mismatch (agent reports a config we didn't push) → no transition.
	// This guards against accidentally marking apply_state from a prior or
	// concurrent rollout's hash.
	svc, store, _ := newTestService(t, "host-1")
	ctx := context.Background()

	r, _ := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")
	svc.ResolveConfigFor(ctx, "host-1", "ship", "linux") // moves to applying

	// Agent reports a totally unrelated hash.
	if err := svc.HandleHeartbeat(ctx, "host-1", "deadbeef00000000", false, ""); err != nil {
		t.Fatalf("HandleHeartbeat: %v", err)
	}

	rows, _, _ := store.ListApplyState(ctx, r.ID, "", paginationAll())
	for _, as := range rows {
		if as.InstanceUID == "host-1" && as.State != rollouts.ApplyApplying {
			t.Errorf("apply_state should stay applying on hash-mismatch heartbeat; got %q", as.State)
		}
	}
}

// stubSemantic is a controllable SemanticValidator for testing the
// rollout flow's semantic-validation gate without a real otelcol binary.
// Returns the canned error (or nil) on every call, and counts calls so
// tests can assert the validator was actually invoked.
type stubSemantic struct {
	err   error
	calls int
}

func (s *stubSemantic) Validate(_ context.Context, _ string) error {
	s.calls++
	return s.err
}

// TestCreateRunsSemanticValidatorWhenWired confirms a Service with a
// SemanticValidator wired in calls it on every Create — the v1.0 cut-line
// guarantee that "a config that fails semantic validation cannot enter
// Canary" (plan §3.2) requires the validator to actually run.
func TestCreateRunsSemanticValidatorWhenWired(t *testing.T) {
	svc, _, _ := newTestService(t, "host-1")
	stub := &stubSemantic{err: nil}
	svc.SetSemanticValidator(stub)
	ctx := context.Background()

	r, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if stub.calls != 1 {
		t.Errorf("semantic validator calls = %d, want 1", stub.calls)
	}
	if r.State != rollouts.StateCanary {
		t.Errorf("rollout State = %q, want canary (semantic passed)", r.State)
	}
}

// TestCreateAbortsOnSemanticValidationFailure confirms a semantic
// validator returning a non-nil error aborts the rollout with the
// distinct semantic_validate_failed reason — the operator sees the
// loader's stderr in the publish dialog and post-incident search can
// pull semantic-failed configs separately from structural failures.
func TestCreateAbortsOnSemanticValidationFailure(t *testing.T) {
	svc, _, _ := newTestService(t, "host-1")
	loaderMsg := "error decoding 'receivers/otlp': missing required field 'protocols'"
	svc.SetSemanticValidator(&stubSemantic{err: errors.New(loaderMsg)})
	ctx := context.Background()

	r, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")

	var ev *rollouts.ErrValidate
	if !errors.As(err, &ev) {
		t.Fatalf("err = %v, want *ErrValidate", err)
	}
	if r == nil {
		t.Fatalf("rollout should still be returned (in aborted state) so the audit chain is forensic")
	}
	if r.State != rollouts.StateAborted {
		t.Errorf("State = %q, want aborted", r.State)
	}
	if r.AbortReason != rollouts.AbortSemanticValidateFailed {
		t.Errorf("AbortReason = %q, want semantic_validate_failed", r.AbortReason)
	}
	if !contains(ev.Detail, loaderMsg) {
		t.Errorf("ErrValidate.Detail = %q, want substring %q (loader stderr should reach the operator)", ev.Detail, loaderMsg)
	}
}

// TestCreateSkipsSemanticWhenNotWired confirms a Service with no
// SemanticValidator falls through to v0.2-compat behavior — structural
// validation alone gates the rollout. This is the "binary not configured"
// path; the warning is logged at startup, not per Create.
func TestCreateSkipsSemanticWhenNotWired(t *testing.T) {
	svc, _, _ := newTestService(t, "host-1")
	// No SetSemanticValidator call: s.semantic stays nil.
	ctx := context.Background()

	r, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if r.State != rollouts.StateCanary {
		t.Errorf("rollout State = %q, want canary (no semantic = pass through)", r.State)
	}
}

// TestCreateSkipsSemanticWhenStructuralFails confirms structural failure
// short-circuits before semantic runs. The two stages share an audit
// path but the semantic call is wasted work if structural already
// rejected the YAML, so the wiring optimizes for the common case.
func TestCreateSkipsSemanticWhenStructuralFails(t *testing.T) {
	svc, _, _ := newTestService(t, "host-1")
	stub := &stubSemantic{err: nil}
	svc.SetSemanticValidator(stub)
	ctx := context.Background()

	r, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: invalidYAML, Kind: rollouts.KindPhased,
	}, "alice")

	var ev *rollouts.ErrValidate
	if !errors.As(err, &ev) {
		t.Fatalf("err = %v, want *ErrValidate", err)
	}
	if r.AbortReason != rollouts.AbortValidateFailed {
		t.Errorf("AbortReason = %q, want validate_failed (structural)", r.AbortReason)
	}
	if stub.calls != 0 {
		t.Errorf("semantic validator calls = %d, want 0 (structural should short-circuit)", stub.calls)
	}
}

// contains is a tiny strings.Contains shim avoiding a strings import in
// this single use site. Kept private to the test file.
func contains(s, substr string) bool {
	if substr == "" {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestListApplyStateHostRefFilter confirms the optional hostRef arg
// restricts results to a single instance_uid — used by the host-drawer
// in-flight strip so per-poll fetch is one row instead of up to a
// 5000-row page. Empty hostRef preserves the v0.2 "all rows" behavior.
func TestListApplyStateHostRefFilter(t *testing.T) {
	svc, store, _ := newTestService(t, "host-1", "host-2", "host-3", "host-4")
	ctx := context.Background()

	r, err := svc.Create(ctx, rollouts.CreateRequest{
		ScopeKind: rollouts.ScopeProductVariant, ScopeRef: "ship/linux",
		ConfigName: "v1", ConfigYAML: validYAML, Kind: rollouts.KindPhased,
	}, "alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Empty hostRef: all four hosts should return.
	all, _, err := store.ListApplyState(ctx, r.ID, "", paginationAll())
	if err != nil {
		t.Fatalf("ListApplyState (no filter): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("ListApplyState (no filter) returned %d rows, want 4", len(all))
	}

	// hostRef = "host-2": exactly one row, with that uid.
	one, _, err := store.ListApplyState(ctx, r.ID, "host-2", paginationAll())
	if err != nil {
		t.Fatalf("ListApplyState (host-2): %v", err)
	}
	if len(one) != 1 {
		t.Fatalf("ListApplyState (host-2) returned %d rows, want 1", len(one))
	}
	if one[0].InstanceUID != "host-2" {
		t.Errorf("filtered row uid = %q, want host-2", one[0].InstanceUID)
	}

	// hostRef pointing at a host not in the rollout: zero rows, no error.
	none, _, err := store.ListApplyState(ctx, r.ID, "host-not-in-rollout", paginationAll())
	if err != nil {
		t.Fatalf("ListApplyState (missing host): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("ListApplyState (missing host) returned %d rows, want 0", len(none))
	}
}

// ─────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────

// canaryHosts returns the instance_uids of apply_state rows where
// is_canary=true, used by tests that need to walk those rows manually
// (no OpAMP push integration yet).
func canaryHosts(t *testing.T, ctx context.Context, store *rollouts.Store, rolloutID int64) []string {
	t.Helper()
	all, _, err := store.ListApplyState(ctx, rolloutID, "", paginationAll())
	if err != nil {
		t.Fatalf("ListApplyState: %v", err)
	}
	var out []string
	for _, as := range all {
		if as.IsCanary {
			out = append(out, as.InstanceUID)
		}
	}
	return out
}

// paginationAll returns a Params that fetches everything (limit large).
// Only used in tests — production callers go through the handler layer
// which applies sensible defaults.
func paginationAll() pagination.Params {
	return pagination.Params{Limit: 1000}
}
