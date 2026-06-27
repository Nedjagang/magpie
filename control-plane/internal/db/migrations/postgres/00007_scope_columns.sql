-- +goose Up
--
-- Postgres equivalent of migrations/sqlite/00007_scope_columns.sql.
-- Same SQL works in PG: ALTER TABLE ADD COLUMN with DEFAULT, ||
-- string concatenation, partial-index syntax all match.
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
