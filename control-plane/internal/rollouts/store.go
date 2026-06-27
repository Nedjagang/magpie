package rollouts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/magpie-project/magpie/control-plane/internal/db"
	"github.com/magpie-project/magpie/control-plane/internal/pagination"
)

// Store is the persistence layer for the rollouts and apply_state tables
// (created by migration 00008). All access is through *db.Conn so query
// strings can stay portable (`?` placeholders rebind to `$N` for
// Postgres at the boundary).
type Store struct {
	conn *db.Conn
}

func NewStore(c *db.Conn) *Store { return &Store{conn: c} }

// ─────────────────────────────────────────────────────────────────────
// Rollouts table
// ─────────────────────────────────────────────────────────────────────

// Insert writes a new rollout row. On success r.ID is populated from the
// generated id. Uses INSERT ... RETURNING so it works on both backends
// (LastInsertId is unsupported on pgx).
func (s *Store) Insert(ctx context.Context, r *Rollout) error {
	priorPtr := nullableInt64(r.PriorConfigID)

	err := s.conn.QueryRow(ctx, `
		INSERT INTO rollouts (
			scope_kind, scope_ref, config_id, prior_config_id,
			rollout_kind, state, prev_state,
			canary_pct, canary_count, canary_size,
			soak_seconds, gate_mode, gate_passed_at,
			created_at, created_by,
			validated_at, canary_at, soak_at, promoted_at, done_at,
			paused_at, aborted_at, abort_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id
	`,
		string(r.ScopeKind), r.ScopeRef, r.ConfigID, priorPtr,
		string(r.Kind), string(r.State), nullableState(r.PrevState),
		nullableInt(r.CanaryPct), nullableInt(r.CanaryCount), nullableInt(r.CanarySize),
		r.SoakSeconds, string(r.GateMode), nullableTime(r.GatePassedAt),
		r.CreatedAt, r.CreatedBy,
		nullableTime(r.ValidatedAt), nullableTime(r.CanaryAt), nullableTime(r.SoakAt),
		nullableTime(r.PromotedAt), nullableTime(r.DoneAt),
		nullableTime(r.PausedAt), nullableTime(r.AbortedAt), string(r.AbortReason),
	).Scan(&r.ID)
	if err != nil {
		return fmt.Errorf("insert rollout: %w", err)
	}
	return nil
}

// Update writes the rollout row in place. Used by the state machine for
// every legal transition. The id and immutable fields (ScopeKind,
// ScopeRef, ConfigID, PriorConfigID, Kind, CreatedAt, CreatedBy) are not
// touched even if the caller mutated them — keeps state-machine flow
// honest.
func (s *Store) Update(ctx context.Context, r *Rollout) error {
	_, err := s.conn.Exec(ctx, `
		UPDATE rollouts SET
			state = ?, prev_state = ?,
			canary_pct = ?, canary_count = ?, canary_size = ?,
			soak_seconds = ?, gate_mode = ?, gate_passed_at = ?,
			validated_at = ?, canary_at = ?, soak_at = ?, promoted_at = ?, done_at = ?,
			paused_at = ?, aborted_at = ?, abort_reason = ?
		WHERE id = ?
	`,
		string(r.State), nullableState(r.PrevState),
		nullableInt(r.CanaryPct), nullableInt(r.CanaryCount), nullableInt(r.CanarySize),
		r.SoakSeconds, string(r.GateMode), nullableTime(r.GatePassedAt),
		nullableTime(r.ValidatedAt), nullableTime(r.CanaryAt), nullableTime(r.SoakAt),
		nullableTime(r.PromotedAt), nullableTime(r.DoneAt),
		nullableTime(r.PausedAt), nullableTime(r.AbortedAt), string(r.AbortReason),
		r.ID,
	)
	if err != nil {
		return fmt.Errorf("update rollout %d: %w", r.ID, err)
	}
	return nil
}

// Get returns the rollout with the given id.
func (s *Store) Get(ctx context.Context, id int64) (*Rollout, bool, error) {
	r, err := scanRollout(s.conn.QueryRow(ctx, selectRolloutSQL+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return r, true, nil
}

// ListFilter narrows the List query. Empty fields are ignored.
type ListFilter struct {
	ScopeKind ScopeKind // optional — empty matches both
	ScopeRef  string    // optional
	InFlight  bool      // when true, filter to non-terminal states (validating/canary/soak/promoting/paused)
}

// List returns rollouts newest-first, paginated.
//
// Pagination is on `id` (DESC), matching the audit/configs Stores —
// cursor is the raw last id of the previous page; "id < cursor" walks
// older rollouts.
func (s *Store) List(ctx context.Context, f ListFilter, p pagination.Params) ([]Rollout, string, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = pagination.DefaultLimit
	}
	fetchN := limit + 1

	q := selectRolloutSQL
	args := []any{}
	where := ""
	if f.ScopeKind != "" {
		where += ` WHERE scope_kind = ?`
		args = append(args, string(f.ScopeKind))
		if f.ScopeRef != "" {
			where += ` AND scope_ref = ?`
			args = append(args, f.ScopeRef)
		}
	} else if f.ScopeRef != "" {
		where += ` WHERE scope_ref = ?`
		args = append(args, f.ScopeRef)
	}
	if f.InFlight {
		if where == "" {
			where += ` WHERE `
		} else {
			where += ` AND `
		}
		where += `state NOT IN ('done', 'aborted')`
	}
	if p.Cursor != "" {
		cursorID, perr := strconv.ParseInt(p.Cursor, 10, 64)
		if perr != nil {
			return nil, "", fmt.Errorf("invalid rollouts cursor: %w", perr)
		}
		if where == "" {
			where += ` WHERE id < ?`
		} else {
			where += ` AND id < ?`
		}
		args = append(args, cursorID)
	}

	rows, err := s.conn.Query(ctx, q+where+` ORDER BY id DESC LIMIT ?`, append(args, fetchN)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := make([]Rollout, 0, limit)
	for rows.Next() {
		r, err := scanRollout(rowAdapter{rows})
		if err != nil {
			return nil, "", err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(out) > limit {
		out = out[:limit]
		nextCursor = strconv.FormatInt(out[len(out)-1].ID, 10)
	}
	return out, nextCursor, nil
}

// LiveConfigID returns the config_id of the most recent Done rollout for
// the given Scope, or nil if no rollout has reached Done for this Scope
// yet (brand-new Scope, or all rollouts so far have aborted).
//
// This is what the resolution priority chain in spec §7 reduces to for
// any one Scope: the live Config is the Config of the most recent Done
// rollout. The partial index `rollouts_scope_done_idx` (migration 00008)
// makes this an O(log n) lookup.
func (s *Store) LiveConfigID(ctx context.Context, kind ScopeKind, ref string) (*int64, error) {
	var id int64
	err := s.conn.QueryRow(ctx, `
		SELECT config_id FROM rollouts
		WHERE scope_kind = ? AND scope_ref = ? AND state = 'done'
		ORDER BY done_at DESC LIMIT 1
	`, string(kind), ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// FindInFlight returns a non-terminal rollout for the given Scope, if
// any. Used by Service.Create to enforce the at-most-one-non-terminal-
// rollout-per-Scope concurrency rule (spec §3 concurrency rule).
func (s *Store) FindInFlight(ctx context.Context, kind ScopeKind, ref string) (*Rollout, error) {
	r, err := scanRollout(s.conn.QueryRow(ctx, selectRolloutSQL+`
		WHERE scope_kind = ? AND scope_ref = ?
		  AND state NOT IN ('done', 'aborted')
		ORDER BY id DESC LIMIT 1
	`, string(kind), ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// PendingForHost returns the apply_state row + its parent rollout
// for the highest-priority non-terminal rollout targeting this host.
//
// "Pending" here means the apply_state is in `pending` or `applying` —
// i.e. the rollout's effect on this host hasn't completed yet, so the
// host should still be receiving the rollout's Config (not the live
// Config for its Scope).
//
// Priority order: instance-scoped rollouts win over product+variant-
// scoped rollouts (Shape 1 override semantics from spec §7); within
// the same scope kind, newer rollouts win (id DESC).
//
// Returns (nil, nil) if no non-terminal rollout has a pending/applying
// apply_state row for this host — caller falls back to the live
// Config resolution chain.
func (s *Store) PendingForHost(ctx context.Context, instanceUID string) (rolloutID int64, configID int64, applyState ApplyStateValue, found bool, err error) {
	// CASE expression: instance-scope priority (1) before product+variant (0).
	// Cross-dialect: SQLite + Postgres both evaluate `r.scope_kind = 'instance'`
	// to 1/0 (SQLite) or true/false (Postgres); ORDER BY DESC works either way.
	row := s.conn.QueryRow(ctx, `
		SELECT a.rollout_id, r.config_id, a.state
		FROM apply_state a
		JOIN rollouts r ON r.id = a.rollout_id
		WHERE a.instance_uid = ?
		  AND r.state NOT IN ('done', 'aborted')
		  AND a.state IN ('pending', 'applying')
		ORDER BY (r.scope_kind = 'instance') DESC, r.id DESC
		LIMIT 1
	`, instanceUID)
	var stateStr string
	err = row.Scan(&rolloutID, &configID, &stateStr)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, "", false, nil
	}
	if err != nil {
		return 0, 0, "", false, err
	}
	return rolloutID, configID, ApplyStateValue(stateStr), true, nil
}

// NonTerminalForHost returns every non-terminal rollout that has an
// apply_state row for this host (any state — pending, applying, applied,
// or failed). Used by HandleHeartbeat to find rollouts whose target
// hash should be compared against the agent's reported applied hash.
//
// Newer rollouts first.
func (s *Store) NonTerminalForHost(ctx context.Context, instanceUID string) ([]Rollout, error) {
	rows, err := s.conn.Query(ctx, selectRolloutSQL+`
		WHERE id IN (
		    SELECT a.rollout_id FROM apply_state a
		    WHERE a.instance_uid = ?
		)
		AND state NOT IN ('done', 'aborted')
		ORDER BY id DESC
	`, instanceUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rollout
	for rows.Next() {
		r, err := scanRollout(rowAdapter{rows})
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// NonTerminal returns all rollouts in non-terminal states. Used by the
// background ticker to find rollouts that need AdvancePhase ticked.
//
// At v1.0 scale (active rollouts ≤ a few dozen typically) this is cheap;
// the rollouts_state_created_idx index covers it.
func (s *Store) NonTerminal(ctx context.Context) ([]int64, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id FROM rollouts
		WHERE state NOT IN ('done', 'aborted')
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ApplyStateCounts returns the current count of apply_state rows by
// state, scoped to non-terminal rollouts only. Used by the metrics
// collector for `magpie_apply_state_total{state}`. Rows from completed
// or aborted rollouts are excluded — the metric represents what's
// in flight, not historical totals.
//
// Returned map keys: "pending", "applying", "applied", "failed".
// Missing keys mean zero of that state.
func (s *Store) ApplyStateCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT a.state, COUNT(*)
		FROM apply_state a
		JOIN rollouts r ON r.id = a.rollout_id
		WHERE r.state NOT IN ('done', 'aborted')
		GROUP BY a.state
	`)
	if err != nil {
		return nil, fmt.Errorf("apply state counts: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		out[state] = count
	}
	return out, rows.Err()
}

// RolloutPhaseCounts returns the current count of non-terminal rollouts
// grouped by state. Used by the metrics collector for
// `magpie_rollout_phase_active{phase}`. Done + aborted are excluded —
// the metric represents what's currently advancing.
//
// Returned map keys are the rollout state strings: "validating",
// "canary", "soak", "promoting", "paused". Missing keys mean zero.
func (s *Store) RolloutPhaseCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT state, COUNT(*)
		FROM rollouts
		WHERE state NOT IN ('done', 'aborted')
		GROUP BY state
	`)
	if err != nil {
		return nil, fmt.Errorf("rollout phase counts: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		out[state] = count
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────
// ApplyState table
// ─────────────────────────────────────────────────────────────────────

// InsertApplyStates batch-inserts pending apply_state rows for a list of
// hosts. Used at canary-start and promote-start; both call paths set
// is_canary appropriately. Idempotent on (rollout_id, instance_uid)
// conflict — a re-issue of the same call is a no-op rather than a hard
// error, which matters when magpied restarts mid-rollout and re-derives
// the same set of hosts.
func (s *Store) InsertApplyStates(ctx context.Context, rolloutID int64, hosts []string, isCanary bool) error {
	if len(hosts) == 0 {
		return nil
	}
	now := time.Now().UTC()
	canaryFlag := 0
	if isCanary {
		canaryFlag = 1
	}
	for _, uid := range hosts {
		_, err := s.conn.Exec(ctx, `
			INSERT INTO apply_state (
				rollout_id, instance_uid, state, is_canary,
				attempt_count, applied_hash, last_error,
				pushed_at, updated_at
			) VALUES (?, ?, 'pending', ?, 0, '', '', NULL, ?)
			ON CONFLICT(rollout_id, instance_uid) DO NOTHING
		`, rolloutID, uid, canaryFlag, now)
		if err != nil {
			return fmt.Errorf("insert apply_state for rollout=%d host=%s: %w", rolloutID, uid, err)
		}
	}
	return nil
}

// UpdateApplyState transitions one host's row in response to an OpAMP
// heartbeat (or a synthetic transition for tests / push-time stubs).
// Used during canary, soak (no-op transitions), and promote phases.
//
// Not wired into OpAMP heartbeat handling yet (deferred to next turn);
// tests use this directly to walk the state machine end-to-end.
func (s *Store) UpdateApplyState(ctx context.Context, rolloutID int64, instanceUID string, newState ApplyStateValue, appliedHash, lastError string) error {
	_, err := s.conn.Exec(ctx, `
		UPDATE apply_state SET
			state = ?,
			applied_hash = ?,
			last_error = ?,
			attempt_count = attempt_count + 1,
			updated_at = ?
		WHERE rollout_id = ? AND instance_uid = ?
	`, string(newState), appliedHash, lastError, time.Now().UTC(), rolloutID, instanceUID)
	if err != nil {
		return fmt.Errorf("update apply_state rollout=%d host=%s: %w", rolloutID, instanceUID, err)
	}
	return nil
}

// Aggregate returns counts of apply_state rows for a rollout grouped by
// state + canary/promote split. Used by the publish dialog's live-progress
// view and the Rollouts UI (spec §10 of publish-dialog-spec).
func (s *Store) Aggregate(ctx context.Context, rolloutID int64) (ApplyAggregate, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT state, is_canary, COUNT(*)
		FROM apply_state WHERE rollout_id = ?
		GROUP BY state, is_canary
	`, rolloutID)
	if err != nil {
		return ApplyAggregate{}, fmt.Errorf("aggregate apply_state: %w", err)
	}
	defer rows.Close()

	var agg ApplyAggregate
	for rows.Next() {
		var (
			state    string
			isCanary int
			count    int
		)
		if err := rows.Scan(&state, &isCanary, &count); err != nil {
			return ApplyAggregate{}, err
		}
		switch ApplyStateValue(state) {
		case ApplyPending:
			agg.Pending += count
		case ApplyApplying:
			agg.Applying += count
		case ApplyApplied:
			agg.Applied += count
		case ApplyFailed:
			agg.Failed += count
		}
		if isCanary == 1 {
			agg.TotalCanary += count
		} else {
			agg.TotalPromote += count
		}
	}
	return agg, rows.Err()
}

// ListApplyState returns per-host apply state for a rollout, paginated.
// Cursor is the raw instance_uid of the last row of the previous page —
// rows are sorted by uid for stability.
//
// hostRef, when non-empty, restricts the result to a single instance_uid.
// Used by the host drawer's in-flight rollouts strip so per-poll fetch is
// one row instead of up to a 5000-row page; pagination is irrelevant in
// that case but the cursor parameter remains honored for symmetry.
func (s *Store) ListApplyState(ctx context.Context, rolloutID int64, hostRef string, p pagination.Params) ([]ApplyState, string, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = pagination.DefaultLimit
	}
	fetchN := limit + 1

	// Branch on (hostRef, cursor) presence rather than build the SQL
	// dynamically — four short queries are easier to read than one
	// stitched together, and the extra duplication is local.
	var rows *sql.Rows
	var err error
	switch {
	case hostRef != "" && p.Cursor == "":
		rows, err = s.conn.Query(ctx, `
			SELECT rollout_id, instance_uid, state, is_canary,
			       attempt_count, applied_hash, last_error, pushed_at, updated_at
			FROM apply_state WHERE rollout_id = ? AND instance_uid = ?
			ORDER BY instance_uid LIMIT ?
		`, rolloutID, hostRef, fetchN)
	case hostRef != "" && p.Cursor != "":
		rows, err = s.conn.Query(ctx, `
			SELECT rollout_id, instance_uid, state, is_canary,
			       attempt_count, applied_hash, last_error, pushed_at, updated_at
			FROM apply_state WHERE rollout_id = ? AND instance_uid = ? AND instance_uid > ?
			ORDER BY instance_uid LIMIT ?
		`, rolloutID, hostRef, p.Cursor, fetchN)
	case p.Cursor == "":
		rows, err = s.conn.Query(ctx, `
			SELECT rollout_id, instance_uid, state, is_canary,
			       attempt_count, applied_hash, last_error, pushed_at, updated_at
			FROM apply_state WHERE rollout_id = ?
			ORDER BY instance_uid LIMIT ?
		`, rolloutID, fetchN)
	default:
		rows, err = s.conn.Query(ctx, `
			SELECT rollout_id, instance_uid, state, is_canary,
			       attempt_count, applied_hash, last_error, pushed_at, updated_at
			FROM apply_state WHERE rollout_id = ? AND instance_uid > ?
			ORDER BY instance_uid LIMIT ?
		`, rolloutID, p.Cursor, fetchN)
	}
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := make([]ApplyState, 0, limit)
	for rows.Next() {
		var (
			as       ApplyState
			isCanary int
			pushed   sql.NullTime
		)
		if err := rows.Scan(&as.RolloutID, &as.InstanceUID, &as.State, &isCanary,
			&as.AttemptCount, &as.AppliedHash, &as.LastError, &pushed, &as.UpdatedAt); err != nil {
			return nil, "", err
		}
		as.IsCanary = isCanary == 1
		if pushed.Valid {
			t := pushed.Time
			as.PushedAt = &t
		}
		out = append(out, as)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(out) > limit {
		out = out[:limit]
		nextCursor = out[len(out)-1].InstanceUID
	}
	return out, nextCursor, nil
}

// ─────────────────────────────────────────────────────────────────────
// Internal: row scanning helpers
// ─────────────────────────────────────────────────────────────────────

const selectRolloutSQL = `
	SELECT id, scope_kind, scope_ref, config_id, prior_config_id,
	       rollout_kind, state, prev_state,
	       canary_pct, canary_count, canary_size,
	       soak_seconds, gate_mode, gate_passed_at,
	       created_at, created_by,
	       validated_at, canary_at, soak_at, promoted_at, done_at,
	       paused_at, aborted_at, abort_reason
	FROM rollouts
`

// rowScanner abstracts over *sql.Row and *sql.Rows so scanRollout can
// serve both Get (single-row) and List (multi-row) call sites.
type rowScanner interface {
	Scan(dest ...any) error
}

type rowAdapter struct{ r *sql.Rows }

func (a rowAdapter) Scan(dest ...any) error { return a.r.Scan(dest...) }

func scanRollout(rs rowScanner) (*Rollout, error) {
	var (
		r              Rollout
		priorConfigID  sql.NullInt64
		prevState      sql.NullString
		canaryPct      sql.NullInt64
		canaryCount    sql.NullInt64
		canarySize     sql.NullInt64
		gatePassedAt   sql.NullTime
		validatedAt    sql.NullTime
		canaryAt       sql.NullTime
		soakAt         sql.NullTime
		promotedAt     sql.NullTime
		doneAt         sql.NullTime
		pausedAt       sql.NullTime
		abortedAt      sql.NullTime
	)
	if err := rs.Scan(
		&r.ID, &r.ScopeKind, &r.ScopeRef, &r.ConfigID, &priorConfigID,
		&r.Kind, &r.State, &prevState,
		&canaryPct, &canaryCount, &canarySize,
		&r.SoakSeconds, &r.GateMode, &gatePassedAt,
		&r.CreatedAt, &r.CreatedBy,
		&validatedAt, &canaryAt, &soakAt, &promotedAt, &doneAt,
		&pausedAt, &abortedAt, &r.AbortReason,
	); err != nil {
		return nil, err
	}
	if priorConfigID.Valid {
		v := priorConfigID.Int64
		r.PriorConfigID = &v
	}
	if prevState.Valid {
		r.PrevState = State(prevState.String)
	}
	if canaryPct.Valid {
		v := int(canaryPct.Int64)
		r.CanaryPct = &v
	}
	if canaryCount.Valid {
		v := int(canaryCount.Int64)
		r.CanaryCount = &v
	}
	if canarySize.Valid {
		v := int(canarySize.Int64)
		r.CanarySize = &v
	}
	if gatePassedAt.Valid {
		v := gatePassedAt.Time
		r.GatePassedAt = &v
	}
	if validatedAt.Valid {
		v := validatedAt.Time
		r.ValidatedAt = &v
	}
	if canaryAt.Valid {
		v := canaryAt.Time
		r.CanaryAt = &v
	}
	if soakAt.Valid {
		v := soakAt.Time
		r.SoakAt = &v
	}
	if promotedAt.Valid {
		v := promotedAt.Time
		r.PromotedAt = &v
	}
	if doneAt.Valid {
		v := doneAt.Time
		r.DoneAt = &v
	}
	if pausedAt.Valid {
		v := pausedAt.Time
		r.PausedAt = &v
	}
	if abortedAt.Valid {
		v := abortedAt.Time
		r.AbortedAt = &v
	}
	return &r, nil
}

// nullableTime / nullableInt / nullableInt64 / nullableState bridge between
// the Rollout struct's pointer fields and SQL NULL semantics — pass nil
// to write SQL NULL, otherwise the dereferenced value.

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func nullableInt(i *int) any {
	if i == nil {
		return nil
	}
	return *i
}

func nullableInt64(i *int64) any {
	if i == nil {
		return nil
	}
	return *i
}

func nullableState(s State) any {
	if s == "" {
		return nil
	}
	return string(s)
}
