-- +goose Up
--
-- Postgres-dialect equivalent of the SQLite RAISE(ABORT, …) triggers from
-- migrations/sqlite/00006_audit_append_only.sql. PG triggers go through a
-- plpgsql function, so the cheap-in-engine append-only enforcement takes
-- a function + two trigger declarations instead of two inline trigger
-- bodies. Same intent: catch accidents and casual cover-ups; not a
-- substitute for off-host audit shipping (planned, v1.x).
--
-- StatementBegin/End markers tell goose to treat the function body as a
-- single statement — it contains semicolons inside $$…$$ that would
-- otherwise confuse the default ;-splitter.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_log_block_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER audit_log_no_update
BEFORE UPDATE ON audit_log
FOR EACH ROW EXECUTE FUNCTION audit_log_block_mutation();

CREATE TRIGGER audit_log_no_delete
BEFORE DELETE ON audit_log
FOR EACH ROW EXECUTE FUNCTION audit_log_block_mutation();

-- +goose Down
DROP TRIGGER IF EXISTS audit_log_no_delete ON audit_log;
DROP TRIGGER IF EXISTS audit_log_no_update ON audit_log;
DROP FUNCTION IF EXISTS audit_log_block_mutation();
