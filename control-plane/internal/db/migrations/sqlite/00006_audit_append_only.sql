-- +goose Up
--
-- Enforce append-only semantics on audit_log at the engine level. The table
-- is described as "append-only" in code comments, but without these triggers
-- any caller with DB access (including anyone who pivots through the v0.1
-- unauthenticated control plane) can DELETE FROM audit_log and erase their
-- own activity. SQLite has no real INSERT-only privilege, but BEFORE-triggers
-- with RAISE(ABORT) are honored even by direct sqlite3 CLI access.
--
-- This does NOT make the table cryptographically tamper-evident — anyone with
-- write access to magpie.db can still DROP TABLE / replace the file. For that
-- threat use a WORM mount or off-host shipping. This is the cheap, in-engine
-- defense that catches accidents and casual cover-ups.
--
-- +goose StatementBegin
CREATE TRIGGER audit_log_no_update
BEFORE UPDATE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER audit_log_no_delete
BEFORE DELETE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS audit_log_no_delete;
DROP TRIGGER IF EXISTS audit_log_no_update;
