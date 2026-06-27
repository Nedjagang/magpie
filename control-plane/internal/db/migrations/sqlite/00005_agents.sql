-- +goose Up
--
-- Persists the per-agent state the OpAMP registry used to keep only in
-- memory. Without this table, restarting magpied blanks the UI's fleet
-- view — agents reconnect but their first-seen / connected-at / applied-
-- config history is gone. Architecture.md also claimed "state is
-- reconstituted on reconnect", which was only true for labels.
--
-- Shape mirrors opamp.Agent fields 1:1 so internal/agents.Store can upsert
-- and hydrate without mapping surprises. attributes is stored as a JSON
-- blob (TEXT) rather than a separate side table — the field is read-only
-- from the control-plane's perspective (set by the agent via
-- AgentDescription), we never query inside it, and shape is arbitrary
-- per agent.
--
-- healthy uses INTEGER + NULL to preserve the existing three-state
-- semantic in Go (never reported / healthy / unhealthy) — NULL is
-- meaningful here, not a data-quality smell.
CREATE TABLE agents (
    instance_uid         TEXT    PRIMARY KEY,
    attributes_json      TEXT    NOT NULL DEFAULT '{}',
    healthy              INTEGER NULL,              -- 0 / 1 / NULL
    last_status          TEXT    NOT NULL DEFAULT '',
    connected_at         DATETIME NOT NULL,
    last_seen            DATETIME NOT NULL,
    applied_config_hash  TEXT    NOT NULL DEFAULT '',
    config_status        TEXT    NOT NULL DEFAULT '',
    config_error         TEXT    NOT NULL DEFAULT ''
);

-- last_seen is what the UI orders by for "recently active" views and what
-- future cleanup jobs will filter on to prune long-gone agents.
CREATE INDEX agents_last_seen_idx ON agents(last_seen);

-- +goose Down
DROP INDEX IF EXISTS agents_last_seen_idx;
DROP TABLE IF EXISTS agents;
