-- +goose Up
--
-- v1.0 Rollout + ApplyState primitives.
-- Materialises docs/v1.0-rollout-spec.md §10 (schema). No Go logic depends
-- on these tables yet — the state machine (§3 of the spec) and gate
-- evaluator (§4.3) are spec'd but not implemented; landing the schema
-- first is intentional so it's reviewable and stable before the engine
-- code is written.
--
-- Schema notes:
--  * Every state-machine transition is a DB write — magpied keeps no
--    in-memory state that affects Rollout correctness (see spec §10
--    "Recoverability"). Phase-entry timestamps (validated_at, canary_at,
--    soak_at, promoted_at, done_at, paused_at, aborted_at) are written
--    on entry to that state so a crash mid-Rollout reconstructs the
--    timeline from disk.
--  * canary_pct / canary_count are the *requested* parameters; canary_size
--    is the actual size at canary-start (which may be smaller if fewer
--    hosts are connected — see spec §4.2). Storing both lets post-incident
--    review see "the operator asked for 5%, we delivered to N hosts."
--  * gate_passed_at flagged separately from soak_at so manual-mode
--    Rollouts can sit in `soak` with the gate already passed, awaiting
--    operator Promote. NULL = not yet evaluated or not yet passed.
--  * abort_reason carries the typed reason from spec §5 ("operator",
--    "canary_gate_failed", "validate_failed", "validate_timeout",
--    "no_canary_targets"). Stored as TEXT not enum so v1.x can extend
--    without a schema migration.
--
-- CHECK constraints encode the working assumption that Shape 2 (labels)
-- is reserved but not implemented; widening the constraint is the
-- intentional gate when Shape 2 lands.
CREATE TABLE rollouts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    scope_kind      TEXT NOT NULL CHECK (scope_kind IN ('product_variant','instance')),
    scope_ref       TEXT NOT NULL,
    config_id       INTEGER NOT NULL,
    prior_config_id INTEGER,
    rollout_kind    TEXT NOT NULL CHECK (rollout_kind IN ('phased','instant')),
    state           TEXT NOT NULL CHECK (state IN
                      ('validating','canary','soak','promoting','paused','done','aborted')),
    prev_state      TEXT,
    canary_pct      INTEGER,
    canary_count    INTEGER,
    canary_size     INTEGER,
    soak_seconds    INTEGER NOT NULL DEFAULT 300,
    gate_mode       TEXT    NOT NULL DEFAULT 'auto' CHECK (gate_mode IN ('auto','manual')),
    gate_passed_at  DATETIME,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by      TEXT    NOT NULL,
    validated_at    DATETIME,
    canary_at       DATETIME,
    soak_at         DATETIME,
    promoted_at     DATETIME,
    done_at         DATETIME,
    paused_at       DATETIME,
    aborted_at      DATETIME,
    abort_reason    TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (config_id)       REFERENCES configs(id),
    FOREIGN KEY (prior_config_id) REFERENCES configs(id)
);

-- Index for "in-flight rollouts for this Scope" queries — used by the
-- concurrency check on POST /rollouts (spec §3 concurrency rule) and by
-- the Rollouts UI section's filter.
CREATE INDEX rollouts_scope_state_idx ON rollouts(scope_kind, scope_ref, state);

-- Index for the Rollouts UI list ordered by recency.
CREATE INDEX rollouts_state_created_idx ON rollouts(state, created_at DESC);

-- Partial index supporting the live-config-resolution query (spec §10):
--   SELECT config_id FROM rollouts
--   WHERE scope_kind=? AND scope_ref=? AND state='done'
--   ORDER BY done_at DESC LIMIT 1;
-- Constant-time at any rollout-history depth.
CREATE INDEX rollouts_scope_done_idx ON rollouts(scope_kind, scope_ref, done_at DESC)
    WHERE state = 'done';

-- ApplyState: per (rollout, host) state machine (spec §8).
--   pending  — row created, no push sent yet
--   applying — push sent, awaiting agent ack
--   applied  — agent reported effective_config_hash matches target
--   failed   — agent reported RemoteConfigStatus.Failed
--
-- is_canary distinguishes canary subset from promote subset so the gate
-- evaluator (spec §4.3) can compute the canary success rate without
-- counting promote-phase rows.
CREATE TABLE apply_state (
    rollout_id    INTEGER NOT NULL,
    instance_uid  TEXT    NOT NULL,
    state         TEXT    NOT NULL CHECK (state IN ('pending','applying','applied','failed')),
    is_canary     INTEGER NOT NULL CHECK (is_canary IN (0,1)),
    attempt_count INTEGER NOT NULL DEFAULT 0,
    applied_hash  TEXT    NOT NULL DEFAULT '',
    last_error    TEXT    NOT NULL DEFAULT '',
    pushed_at     DATETIME,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (rollout_id, instance_uid),
    FOREIGN KEY (rollout_id) REFERENCES rollouts(id)
);

-- Powers the gate evaluator's aggregate query (count by state) and the
-- per-rollout drawer's "X/Y applied · Z failed" summary.
CREATE INDEX apply_state_rollout_state_idx ON apply_state(rollout_id, state);

-- Powers the host drawer's "rollouts affecting this host, recent first"
-- view (spec §16 — host drawer cross-link).
CREATE INDEX apply_state_host_idx ON apply_state(instance_uid, updated_at DESC);

-- +goose Down
DROP INDEX IF EXISTS apply_state_host_idx;
DROP INDEX IF EXISTS apply_state_rollout_state_idx;
DROP TABLE apply_state;

DROP INDEX IF EXISTS rollouts_scope_done_idx;
DROP INDEX IF EXISTS rollouts_state_created_idx;
DROP INDEX IF EXISTS rollouts_scope_state_idx;
DROP TABLE rollouts;
