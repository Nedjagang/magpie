-- +goose Up
--
-- Postgres-dialect equivalent of migrations/sqlite/00008_rollouts_and_apply_state.sql.
-- See the SQLite version for design rationale on every column; this file
-- is mechanical translation:
--   * INTEGER PRIMARY KEY AUTOINCREMENT → BIGSERIAL PRIMARY KEY
--   * INTEGER (foreign keys, ids) → BIGINT to match BIGSERIAL
--   * DATETIME → TIMESTAMPTZ (UTC-anchored timestamps)
--   * CURRENT_TIMESTAMP → NOW() (both work in PG; NOW() is the convention)
--   * BOOLEAN-as-INTEGER (is_canary) → SMALLINT to keep wire-format identical
--     to the SQLite path (Go reads it as int either way; using PG BOOLEAN
--     would force a code-side type change for marginal benefit)
-- CHECK constraints, partial indexes, FOREIGN KEY syntax all portable.

CREATE TABLE rollouts (
    id              BIGSERIAL PRIMARY KEY,
    scope_kind      TEXT NOT NULL CHECK (scope_kind IN ('product_variant','instance')),
    scope_ref       TEXT NOT NULL,
    config_id       BIGINT NOT NULL,
    prior_config_id BIGINT,
    rollout_kind    TEXT NOT NULL CHECK (rollout_kind IN ('phased','instant')),
    state           TEXT NOT NULL CHECK (state IN
                      ('validating','canary','soak','promoting','paused','done','aborted')),
    prev_state      TEXT,
    canary_pct      INTEGER,
    canary_count    INTEGER,
    canary_size     INTEGER,
    soak_seconds    INTEGER NOT NULL DEFAULT 300,
    gate_mode       TEXT    NOT NULL DEFAULT 'auto' CHECK (gate_mode IN ('auto','manual')),
    gate_passed_at  TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by      TEXT    NOT NULL,
    validated_at    TIMESTAMPTZ,
    canary_at       TIMESTAMPTZ,
    soak_at         TIMESTAMPTZ,
    promoted_at     TIMESTAMPTZ,
    done_at         TIMESTAMPTZ,
    paused_at       TIMESTAMPTZ,
    aborted_at      TIMESTAMPTZ,
    abort_reason    TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (config_id)       REFERENCES configs(id),
    FOREIGN KEY (prior_config_id) REFERENCES configs(id)
);

CREATE INDEX rollouts_scope_state_idx ON rollouts(scope_kind, scope_ref, state);
CREATE INDEX rollouts_state_created_idx ON rollouts(state, created_at DESC);

CREATE INDEX rollouts_scope_done_idx ON rollouts(scope_kind, scope_ref, done_at DESC)
    WHERE state = 'done';

CREATE TABLE apply_state (
    rollout_id    BIGINT   NOT NULL,
    instance_uid  TEXT     NOT NULL,
    state         TEXT     NOT NULL CHECK (state IN ('pending','applying','applied','failed')),
    is_canary     SMALLINT NOT NULL CHECK (is_canary IN (0,1)),
    attempt_count INTEGER  NOT NULL DEFAULT 0,
    applied_hash  TEXT     NOT NULL DEFAULT '',
    last_error    TEXT     NOT NULL DEFAULT '',
    pushed_at     TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (rollout_id, instance_uid),
    FOREIGN KEY (rollout_id) REFERENCES rollouts(id)
);

CREATE INDEX apply_state_rollout_state_idx ON apply_state(rollout_id, state);
CREATE INDEX apply_state_host_idx ON apply_state(instance_uid, updated_at DESC);

-- +goose Down
DROP INDEX IF EXISTS apply_state_host_idx;
DROP INDEX IF EXISTS apply_state_rollout_state_idx;
DROP TABLE apply_state;

DROP INDEX IF EXISTS rollouts_scope_done_idx;
DROP INDEX IF EXISTS rollouts_state_created_idx;
DROP INDEX IF EXISTS rollouts_scope_state_idx;
DROP TABLE rollouts;
