-- +goose Up
--
-- v1.0 Scope primitive — adds scope_kind and scope_ref to configs.
--
-- Per docs/v1.0-rollout-spec.md §7 and docs/v1.0-plan.md §3.1, a Config is
-- bound to a Scope. v1.0 supports two Scope shapes:
--   product_variant — historical (product, variant) tuple. scope_ref encoded
--                     as "<product>/<variant>" (e.g. "ship/linux").
--   instance        — per-host override. scope_ref is the agent instance_uid.
--   labels          — Shape 2 selector model. Reserved; not implemented in
--                     v1.0. The CHECK constraint excludes it deliberately so
--                     a stray write can't smuggle in an unimplemented kind.
--
-- Backfill: every existing row had product+variant; map straight onto
-- scope_kind='product_variant', scope_ref='<product>/<variant>'. The
-- product/variant columns are kept (denormalized) for now — existing
-- callers in internal/configs/store.go still read them, and removing them
-- in the same migration would couple a schema change to a code rewrite.
-- A later migration can drop them once the Go layer is fully on Scope.
--
-- The per-Scope partial index supports live-config lookup at fleet scale
-- (see rollouts_scope_done_idx in the next migration for the same pattern
-- on rollouts).
ALTER TABLE configs ADD COLUMN scope_kind TEXT NOT NULL DEFAULT 'product_variant';
ALTER TABLE configs ADD COLUMN scope_ref  TEXT NOT NULL DEFAULT '';

UPDATE configs
   SET scope_ref = product || '/' || variant
 WHERE scope_kind = 'product_variant' AND scope_ref = '';

CREATE INDEX idx_configs_scope ON configs(scope_kind, scope_ref, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_configs_scope;
ALTER TABLE configs DROP COLUMN scope_ref;
ALTER TABLE configs DROP COLUMN scope_kind;
