-- +goose Up
--
-- See migrations/sqlite/00005_agents.sql for the design rationale; this
-- file is the Postgres-dialect equivalent.
--
-- healthy uses SMALLINT NULL (rather than PG's native BOOLEAN) to preserve
-- the three-state semantic the Go code expects (sql.NullInt64 with values
-- 0/1/NULL). Going to BOOLEAN here would force a Go-side type change at
-- every call site for marginal storage savings — not worth it for v1.0.
-- attributes_json stays TEXT (rather than JSONB) so the wire shape matches
-- SQLite exactly; we never query inside the JSON, so JSONB's index/path
-- features would be unused weight.
CREATE TABLE agents (
    instance_uid         TEXT     PRIMARY KEY,
    attributes_json      TEXT     NOT NULL DEFAULT '{}',
    healthy              SMALLINT NULL,
    last_status          TEXT     NOT NULL DEFAULT '',
    connected_at         TIMESTAMPTZ NOT NULL,
    last_seen            TIMESTAMPTZ NOT NULL,
    applied_config_hash  TEXT     NOT NULL DEFAULT '',
    config_status        TEXT     NOT NULL DEFAULT '',
    config_error         TEXT     NOT NULL DEFAULT ''
);

CREATE INDEX agents_last_seen_idx ON agents(last_seen);

-- +goose Down
DROP INDEX IF EXISTS agents_last_seen_idx;
DROP TABLE IF EXISTS agents;
