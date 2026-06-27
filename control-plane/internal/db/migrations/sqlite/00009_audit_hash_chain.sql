-- +goose Up
--
-- v1.0 hash-chained, structured AuditEvents.
-- Materialises docs/v1.0-rollout-spec.md §11 and docs/v1.0-plan.md §3.5.
-- Extends the existing audit_log table rather than replacing it so v0.2
-- history is preserved and the existing append-only triggers from
-- 00006_audit_append_only.sql still apply to the new columns automatically.
--
-- Hash chain semantics (per spec §11):
--   * prev_hash on row N matches hash on row N-1 (or empty string for the
--     first row written under the v1.0 chain).
--   * hash is computed by the application layer over the canonical
--     serialisation of (id, prev_hash, type, actor, scope_kind, scope_ref,
--     config_ref, host_ref, payload_json, at). The DB does not compute it
--     — keeping it in app code lets us version the canonicalisation
--     without a schema migration. (This is also why hash columns are
--     plain TEXT, not BLOB / fixed-length CHAR.)
--   * Tamper-evidence: the chain catches accidental edits and casual
--     cover-ups. Combined with the BEFORE UPDATE / DELETE triggers from
--     00006, mutating the table requires write access to the file plus
--     re-computing every downstream hash. For stronger evidence the
--     deployment should ship audit rows off-host (planned, v1.x).
--
-- Existing rows are kept. Their prev_hash, hash, type, payload_json all
-- default to '' / '{}' and never participate in the v1.0 chain — the
-- application's chain validator should start at the first row whose hash
-- is non-empty. (Pre-v1.0 rows are still queryable, they just predate
-- the hash-chain invariant.)
--
-- type names track the spec §11 table: RolloutCreated, RolloutInstant,
-- RolloutAdvanced, RolloutPaused, RolloutAborted, RolloutPromoted,
-- RolloutFastPromoted, OverrideApplied, OverrideCleared, LabelChanged,
-- HostDeleted, AuthSucceeded, AuthFailed. Stored as TEXT (not enum) so
-- v1.x can extend without a schema migration. Validation is at the
-- application layer.
ALTER TABLE audit_log ADD COLUMN prev_hash    TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN hash         TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN type         TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN scope_kind   TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN scope_ref    TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN config_ref   INTEGER;
ALTER TABLE audit_log ADD COLUMN host_ref     TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN payload_json TEXT NOT NULL DEFAULT '{}';

-- Powers the Audit UI section's per-type filter (spec §13 of plan, audit
-- query surface). Ordered by (at DESC) so "recent events of type X" is
-- a single index seek.
CREATE INDEX idx_audit_type ON audit_log(type, at DESC);

-- Powers "events affecting this Scope" — used by both the Audit section's
-- scope filter and the Product/Rollout drawers' embedded recent activity.
CREATE INDEX idx_audit_scope_v1 ON audit_log(scope_kind, scope_ref, at DESC);

-- Powers "events affecting this host" — used by the host drawer to answer
-- "what's happened to this machine" without a scroll-and-grep.
CREATE INDEX idx_audit_host ON audit_log(host_ref, at DESC);

-- Powers "events touching this config revision" — used when reviewing
-- rollouts/rollbacks tied to a specific Config row.
CREATE INDEX idx_audit_config ON audit_log(config_ref, at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_audit_config;
DROP INDEX IF EXISTS idx_audit_host;
DROP INDEX IF EXISTS idx_audit_scope_v1;
DROP INDEX IF EXISTS idx_audit_type;
ALTER TABLE audit_log DROP COLUMN payload_json;
ALTER TABLE audit_log DROP COLUMN host_ref;
ALTER TABLE audit_log DROP COLUMN config_ref;
ALTER TABLE audit_log DROP COLUMN scope_ref;
ALTER TABLE audit_log DROP COLUMN scope_kind;
ALTER TABLE audit_log DROP COLUMN type;
ALTER TABLE audit_log DROP COLUMN hash;
ALTER TABLE audit_log DROP COLUMN prev_hash;
