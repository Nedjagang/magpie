-- +goose Up
--
-- Postgres equivalent of migrations/sqlite/00009_audit_hash_chain.sql.
-- ALTER TABLE ADD COLUMN with NOT NULL DEFAULT is portable; PG executes
-- it without rewriting the table (since 11+) so this is fast even on
-- a large audit_log. config_ref is BIGINT (matching configs.id BIGSERIAL).
ALTER TABLE audit_log ADD COLUMN prev_hash    TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN hash         TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN type         TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN scope_kind   TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN scope_ref    TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN config_ref   BIGINT;
ALTER TABLE audit_log ADD COLUMN host_ref     TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN payload_json TEXT NOT NULL DEFAULT '{}';

CREATE INDEX idx_audit_type ON audit_log(type, at DESC);
CREATE INDEX idx_audit_scope_v1 ON audit_log(scope_kind, scope_ref, at DESC);
CREATE INDEX idx_audit_host ON audit_log(host_ref, at DESC);
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
