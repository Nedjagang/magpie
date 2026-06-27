-- +goose Up
CREATE TABLE agent_labels (
    instance_uid TEXT PRIMARY KEY,
    product      TEXT NOT NULL,
    variant      TEXT NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE agent_labels;
